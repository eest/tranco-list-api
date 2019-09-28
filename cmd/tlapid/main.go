package main

import (
	"context"
	"database/sql"
	"encoding/json"
	_ "expvar"
	"flag"
	"fmt"
	"github.com/BurntSushi/toml"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/time/rate"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// ipv4Localhost is the default address we listen on when not configured
	// by the config file.
	ipv4Localhost = "127.0.0.1"

	// serviceName is the general name of this service that is the default name
	// used for databases and such.
	serviceName = "trancolist"
)

// mainConfig holds information read from the config file.
type mainConfig struct {
	Server   serverConfig
	Database databaseConfig
	API      apiConfig
}

// serverConfig contains settings regarding the running server.
type serverConfig struct {
	Address              string
	Port                 int
	RateLimit            rate.Limit
	BurstLimit           int
	LimitCleanupAge      string
	LimitCleanupInterval string
	HostWhitelist        []string
	StatsAddress         string
	StatsPort            int
	DevAddress           string
	DevPort              int
}

// databaseConfig contains settings for the database connection.
type databaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
}

// apiConfig contains settings regarding the Tranco list API
type apiConfig struct {
	ReferenceBaseURL string
	RankMin          int64
	RankMax          int64
	CountMin         int64
	CountMax         int64
	DefaultStart     int64
	DefaultCount     int64
	Path             string
}

// readConfig parses the supplied configuration file or falls back to
// default settings.
func readConfig(configFile *string) *mainConfig {
	config := newConfig()

	if *configFile != "" {
		if _, err := toml.DecodeFile(*configFile, config); err != nil {
			log.Fatalf("TOML decoding failed: %s", err)
		}
	}

	return config
}

// NewConfig returns default configuration
func newConfig() *mainConfig {
	return &mainConfig{
		Server: serverConfig{
			Address:              ipv4Localhost,
			Port:                 443,
			RateLimit:            1.0,
			BurstLimit:           1,
			LimitCleanupInterval: "1m",
			LimitCleanupAge:      "1m",
			StatsAddress:         ipv4Localhost,
			StatsPort:            8181,
			DevAddress:           ipv4Localhost,
			DevPort:              8080,
		},
		Database: databaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     serviceName,
			Password: "",
			DBName:   serviceName,
			SSLMode:  "verify-full",
		},
		API: apiConfig{
			ReferenceBaseURL: "https://tranco-list.eu/list",
			RankMin:          1,
			RankMax:          1000000,
			CountMin:         1,
			CountMax:         100,
			DefaultStart:     1,
			DefaultCount:     10,
			Path:             "/api",
		},
	}
}

// apiResponse is used to build the JSON payload sent to clients
type apiResponse struct {
	List         string      `json:"list"`
	LastModified string      `json:"last-modified"`
	Reference    string      `json:"reference"`
	Sites        []siteEntry `json:"sites"`
}

// siteEntry contains data related to each site in the JSON payload
type siteEntry struct {
	Site string `json:"site"`
	Rank int64  `json:"rank"`
}

// openDB creates and validates the DB connection
func openDB(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	// Test connection to database. This speeds up the initial query from a
	// client after the service has started.
	err = db.Ping()
	if err != nil {
		return nil, err
	}

	return db, nil
}

// latestListID returns the DB id and name of the most recently added list
func latestListID(tx *sql.Tx) (int64, string, time.Time, error) {

	var id int64
	var name string
	var t time.Time
	err := tx.QueryRow("SELECT id, name, ts FROM lists ORDER BY ts DESC LIMIT 1").Scan(&id, &name, &t)
	if err != nil {
		return 0, "", time.Time{}, err
	}

	return id, name, t, nil
}

// getListSites return all site entries related to the given list limited by
// the start and count boundaries from the DB
func getListSites(tx *sql.Tx, listID, start, count int64) ([]siteEntry, error) {

	var rank int64
	var site string

	rows, err := tx.Query(`SELECT rank, site FROM sites WHERE list_id = $1 AND rank >= $2 ORDER BY rank ASC LIMIT $3`, listID, start, count)
	if err != nil {
		return []siteEntry{}, fmt.Errorf("getListSites SELECT failed: %s", err)
	}
	defer rows.Close()

	sites := []siteEntry{}
	for rows.Next() {
		err = rows.Scan(&rank, &site)
		if err != nil {
			return []siteEntry{}, fmt.Errorf("getListSites Scan failed: %s", err)
		}

		sites = append(sites, siteEntry{Rank: rank, Site: site})
	}

	return sites, nil
}

// getListSiteRank fetches the site matching the given rank on the specified list from the DB
func getListSiteRank(tx *sql.Tx, listID, rank int64) ([]siteEntry, error) {

	var site string

	err := tx.QueryRow(`SELECT site FROM sites WHERE list_id = $1 AND rank = $2`, listID, rank).Scan(&site)
	if err != nil {
		return []siteEntry{}, fmt.Errorf("getListSiteRank SELECT failed: %s", err)
	}

	return []siteEntry{siteEntry{Rank: rank, Site: site}}, nil
}

// getListSiteName fetches the site matching the given name on the specified list from the DB
func getListSiteName(tx *sql.Tx, listID int64, site string) ([]siteEntry, error) {

	var rank int64

	err := tx.QueryRow(`SELECT rank FROM sites WHERE list_id = $1 AND site = $2`, listID, site).Scan(&rank)
	switch {
	case err == sql.ErrNoRows:
		// Not an error, we only return an empty site list
		return []siteEntry{}, nil
	case err != nil:
		return []siteEntry{}, fmt.Errorf("getListSiteName SELECT failed: %s", err)
	}

	return []siteEntry{siteEntry{Rank: rank, Site: site}}, nil
}

// sitesPayload builds a JSON payload returned when handling a "sites" request
func sitesPayload(db *sql.DB, start, count int64, timeLoc *time.Location, config *mainConfig) (apiResponse, error) {
	// Make sure we have a consistent view of the database between SELECTs in
	// case the database is updated under our feet.

	// https://www.postgresql.org/docs/9.1/transaction-iso.html:
	//
	// The Repeatable Read isolation level only sees data committed before the
	// transaction began; it never sees either uncommitted data or changes
	// committed during transaction execution by concurrent transactions.
	// (However, the query does see the effects of previous updates executed
	// within its own transaction, even though they are not yet committed.)
	// This is a stronger guarantee than is required by the SQL standard for
	// this isolation level, and prevents all of the phenomena described in
	// Table 13-1. As mentioned above, this is specifically allowed by the
	// standard, which only describes the minimum protections each isolation
	// level must provide.
	// [...]
	// Note that only updating transactions might need to be retried; read-only
	// transactions will never have serialization conflicts.
	tx, err := db.BeginTx(
		context.Background(),
		&sql.TxOptions{
			Isolation: sql.LevelRepeatableRead,
			ReadOnly:  true,
		},
	)

	listID, name, ts, err := latestListID(tx)
	if err != nil {
		if err := tx.Rollback(); err != nil {
			log.Printf("latestListID: rollback failed: %s", err)
		}
		// Return actual latestListID error here
		return apiResponse{}, err
	}

	sites, err := getListSites(tx, listID, start, count)
	if err != nil {
		if err := tx.Rollback(); err != nil {
			log.Printf("latestListSites rollback failed: %s", err)
		}
		// Return actual getListSites error here
		return apiResponse{}, err
	}

	// We are done with database queries, cancel the transaction.
	err = tx.Rollback()
	if err != nil {
		return apiResponse{}, err
	}

	return apiResponse{
		List:         name,
		LastModified: ts.In(timeLoc).Format(time.RFC1123), // Format matches Last-Modified header for zip file
		Reference:    fmt.Sprintf("%s/%s", config.API.ReferenceBaseURL, name),
		Sites:        sites,
	}, nil

}

// siteWithRankPayload builds the payload returned when requesting a specific rank
func siteWithRankPayload(db *sql.DB, rank int64, timeLoc *time.Location, config *mainConfig) (apiResponse, error) {
	// See sitesPayload() for explanation regarding sql.LevelRepeatableRead isolation level.
	tx, err := db.BeginTx(
		context.Background(),
		&sql.TxOptions{
			Isolation: sql.LevelRepeatableRead,
			ReadOnly:  true,
		},
	)

	listID, name, ts, err := latestListID(tx)
	if err != nil {
		if err := tx.Rollback(); err != nil {
			log.Printf("latestListID: rollback failed: %s", err)
		}
		// Return actual latestListID error here.
		return apiResponse{}, err
	}

	sites, err := getListSiteRank(tx, listID, rank)
	if err != nil {
		if err := tx.Rollback(); err != nil {
			log.Printf("getListSiteRank: rollback failed: %s", err)
		}
		// Return getListSiteRank error here.
		return apiResponse{}, err
	}

	// We are done with database queries, cancel the transaction.
	err = tx.Rollback()
	if err != nil {
		return apiResponse{}, err
	}

	return apiResponse{
		List:         name,
		LastModified: ts.In(timeLoc).Format(time.RFC1123), // Format matches Last-Modified header for zip file
		Reference:    fmt.Sprintf("%s/%s", config.API.ReferenceBaseURL, name),
		Sites:        sites,
	}, nil

}

// siteWithNamePayload builds the payload returned when requesting a specific name
func siteWithNamePayload(db *sql.DB, site string, timeLoc *time.Location, config *mainConfig) (apiResponse, error) {
	// See sitesPayload() for explanation regarding sql.LevelRepeatableRead isolation level.
	tx, err := db.BeginTx(
		context.Background(),
		&sql.TxOptions{
			Isolation: sql.LevelRepeatableRead,
			ReadOnly:  true,
		},
	)

	listID, name, ts, err := latestListID(tx)
	if err != nil {
		if err := tx.Rollback(); err != nil {
			log.Printf("latestListID: rollback failed: %s", err)
		}
		// Return actual latestListID error here
		return apiResponse{}, err
	}

	sites, err := getListSiteName(tx, listID, site)
	if err != nil {
		if err := tx.Rollback(); err != nil {
			log.Printf("getListSiteName: rollback failed: %s", err)
		}
		// Return actual getListSiteName error here
		return apiResponse{}, err
	}

	// We are done with database queries, cancel the transaction.
	err = tx.Rollback()
	if err != nil {
		return apiResponse{}, err
	}

	return apiResponse{
		List:         name,
		LastModified: ts.In(timeLoc).Format(time.RFC1123), // Format matches Last-Modified header for zip file
		Reference:    fmt.Sprintf("%s/%s", config.API.ReferenceBaseURL, name),
		Sites:        sites,
	}, nil

}

// loggingResponswriter overrides the http.ResponseWriter method so
// we can record the returned status code and amount of data sent.
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
	size       int64
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func (lrw *loggingResponseWriter) Write(data []byte) (int, error) {
	lrw.size = lrw.size + int64(len(data))
	return lrw.ResponseWriter.Write(data)
}

// muxWrapper receives all requests before handing it over to the default net/http mux.
// This is where things like logging is handled since we want to log all requests.
func muxWrapper(handler http.Handler, rl *rateLimit) http.HandlerFunc {
	serverLog := log.New(os.Stderr, "", 0)

	return func(w http.ResponseWriter, r *http.Request) {

		passToHandler := true
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// The req.r.RemoteAddr string includes both the address and
		// port and we need the address
		remoteHostIP, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			log.Printf("unable to parse RemoteAddr: %s", err)
			http.Error(lrw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			passToHandler = false
		}

		// Check rate limit before delegating request.
		if !rl.allow(remoteHostIP) {
			http.Error(lrw, http.StatusText(http.StatusTooManyRequests), http.StatusTooManyRequests)
			passToHandler = false
		}

		if passToHandler {
			handler.ServeHTTP(lrw, r)
		}

		var ua string
		var referer string

		if r.Header.Get("Referer") == "" {
			referer = "-"
		} else {
			referer = "\"" + r.Header.Get("Referer") + "\""
		}

		if r.Header.Get("User-Agent") == "" {
			ua = "-"
		} else {
			ua = "\"" + r.Header.Get("User-Agent") + "\""
		}

		// Apache combined log format:
		// LogFormat "%h %l %u %t \"%r\" %>s %b \"%{Referer}i\" \"%{User-agent}i\"" combined
		serverLog.Printf(
			"%s - - %s \"%s %s %s\" %d %d %s %s",
			remoteHostIP,
			time.Now().Format("02/Jan/2006:15:04:05 -0700"),
			r.Method,
			r.URL.Path,
			r.Proto,
			lrw.statusCode,
			lrw.size,
			referer,
			ua,
		)

	}
}

// request is the struct that is passed to our API handler function, it allows us to
// pass anything we need without depending on global variables.
type request struct {
	w        http.ResponseWriter
	req      *http.Request
	db       *sql.DB
	config   *mainConfig
	timeLoc  *time.Location
	basePath string
}

// handlerWrapper allows us to pass the 'request' struct to our API handler
// while presenting a http.HandlerFunc to the caller.
//
// The resulting call order:
// muxWrapper -> ServeMux -> handlerWrapper -> tlAPIHandler
func handlerWrapper(handler func(*request), db *sql.DB, config *mainConfig, timeLoc *time.Location, basePath string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r := &request{
			w:        w,
			req:      req,
			db:       db,
			config:   config,
			timeLoc:  timeLoc,
			basePath: basePath,
		}

		handler(r)
	}
}

func badRequestHint(w http.ResponseWriter, basePath string) {
	http.Error(
		w,
		fmt.Sprintf("Bad Request: try %s/sites, %s/site/google.com or %s/rank/1", basePath, basePath, basePath),
		http.StatusBadRequest,
	)
}

// tlAPIHandler is the main workhorse of the API, it deals with all requests
// sent to API endpoint.
func tlAPIHandler(r *request) {

	start := r.config.API.DefaultStart
	count := r.config.API.DefaultCount

	var err error

	switch r.req.Method {
	case "GET":
		switch r.req.URL.Path {
		case r.config.API.Path, r.config.API.Path + "/":
			badRequestHint(r.w, r.basePath)
			return
		}

		switch r.req.URL.Path {
		case r.basePath + "/sites", r.basePath + "/sites/":
			startParam := r.req.URL.Query()["start"]
			if len(startParam) > 1 {
				log.Printf("tlAPIHandler more than 1 startParam")
				http.Error(
					r.w,
					http.StatusText(http.StatusBadRequest),
					http.StatusBadRequest,
				)
				return
			}
			if len(startParam) == 1 {
				start, err = strconv.ParseInt(startParam[0], 10, 64)
				if err != nil {
					log.Printf("tlAPIHandler bad startParam: %s", err)
					http.Error(
						r.w,
						http.StatusText(http.StatusBadRequest),
						http.StatusBadRequest,
					)
					return
				}
				if start < r.config.API.RankMin {
					log.Printf("tlAPIHandler startParam less than rankMin: %d", start)
					http.Error(
						r.w,
						fmt.Sprintf("Bad Request: minimum start is %d", r.config.API.RankMin),
						http.StatusBadRequest,
					)
					return
				}
				if start > r.config.API.RankMax {
					log.Printf("tlAPIHandler startParam exceeds rankMax: %d", start)
					http.Error(
						r.w,
						fmt.Sprintf("Bad Request: maximum start is %d", r.config.API.RankMax),
						http.StatusBadRequest,
					)
					return
				}
			}
			countParam := r.req.URL.Query()["count"]
			if len(countParam) > 1 {
				log.Printf("tlAPIHandler more than 1 countParam")
				http.Error(
					r.w,
					http.StatusText(http.StatusBadRequest),
					http.StatusBadRequest,
				)
				return
			}
			if len(countParam) == 1 {
				count, err = strconv.ParseInt(countParam[0], 10, 64)
				if err != nil {
					log.Printf("tlAPIHandler bad countParam: %s", err)
					http.Error(
						r.w,
						http.StatusText(http.StatusBadRequest),
						http.StatusBadRequest,
					)
					return
				}
				if count < r.config.API.CountMin {
					log.Printf("tlAPIHandler countParam less than countMin: %d", count)
					http.Error(
						r.w,
						fmt.Sprintf("Bad Request: minimum count is %d", r.config.API.CountMin),
						http.StatusBadRequest,
					)
					return
				}
				if count > r.config.API.CountMax {
					log.Printf("tlAPIHandler countParam exceeds countMax: %d", count)
					http.Error(
						r.w,
						fmt.Sprintf("Bad Request: maximum count is %d", r.config.API.CountMax),
						http.StatusBadRequest,
					)
					return
				}
			}
			ar, err := sitesPayload(r.db, start, count, r.timeLoc, r.config)
			if err != nil {
				log.Printf("sitesPayload: %s", err)
				http.Error(
					r.w,
					http.StatusText(http.StatusInternalServerError),
					http.StatusInternalServerError,
				)
				return
			}
			r.w.Header().Set("Content-Type", "application/json")
			err = json.NewEncoder(r.w).Encode(ar)
			if err != nil {
				log.Printf("sitesPayload JSON encoding error: %s", err)
				http.Error(
					r.w,
					http.StatusText(http.StatusInternalServerError),
					http.StatusInternalServerError,
				)
				return
			}
			return
		}

		// Throw "Bad Request" if any of the supported paths are called
		// without the necessary parameter at the end.
		switch r.req.URL.Path {
		case r.basePath + "/rank", r.basePath + "/rank/", r.basePath + "/site", r.basePath + "/site/":
			http.Error(
				r.w,
				http.StatusText(http.StatusBadRequest),
				http.StatusBadRequest,
			)
			return
		}

		var apiPartsLen int

		// An API path string of "/api" splits into two parts
		// which we need to skip over, but an API path of "/"
		// also splits into two parts, but we only need to skip
		// over one, so handle that special case here.
		if r.basePath == "" {
			apiPartsLen = 1
		} else {
			apiPartsLen = len(strings.Split(r.config.API.Path, "/"))
		}

		// curl -s 127.0.0.1:8080/api/rank/1
		// 2019/08/31 00:40:28 urlParts: []string{"", "api", "rank", "1"}
		urlParts := strings.Split(r.req.URL.Path, "/")
		urlPartsLen := len(urlParts)
		switch urlPartsLen - apiPartsLen {
		case 2:
			switch urlParts[apiPartsLen] {
			case "rank":
				rank, err := strconv.ParseInt(urlParts[apiPartsLen+1], 10, 64)
				if err != nil {
					log.Printf("tlAPIHandler rank ParseInt: %s", err)
					http.Error(
						r.w,
						http.StatusText(http.StatusBadRequest),
						http.StatusBadRequest,
					)
					return
				}

				if rank < r.config.API.RankMin {
					log.Printf("tlAPIHandler rank below rankMin: %d ", rank)
					http.Error(
						r.w,
						fmt.Sprintf("Bad Request: minimum rank is %d", r.config.API.RankMin),
						http.StatusBadRequest,
					)
					return
				}

				if rank > r.config.API.RankMax {
					log.Printf("tlAPIHandler rankMax exceeded: %d ", rank)
					http.Error(
						r.w,
						fmt.Sprintf("Bad Request: maximum rank is %d", r.config.API.RankMax),
						http.StatusBadRequest,
					)
					return
				}

				ar, err := siteWithRankPayload(r.db, rank, r.timeLoc, r.config)
				if err != nil {
					log.Printf("siteWithRankPayload: %s", err)
					http.Error(
						r.w,
						http.StatusText(http.StatusInternalServerError),
						http.StatusInternalServerError,
					)
					return
				}
				r.w.Header().Set("Content-Type", "application/json")
				err = json.NewEncoder(r.w).Encode(ar)
				if err != nil {
					log.Printf("sitesWithRankPayload JSON encoding error: %s", err)
					http.Error(
						r.w,
						http.StatusText(http.StatusInternalServerError),
						http.StatusInternalServerError,
					)
					return
				}
				return
			case "site":
				site := urlParts[apiPartsLen+1]
				ar, err := siteWithNamePayload(r.db, site, r.timeLoc, r.config)
				if err != nil {
					log.Printf("siteWithNamePayload: %s", err)
					http.Error(
						r.w,
						http.StatusText(http.StatusInternalServerError),
						http.StatusInternalServerError,
					)
					return
				}
				r.w.Header().Set("Content-Type", "application/json")
				err = json.NewEncoder(r.w).Encode(ar)
				if err != nil {
					log.Printf("siteWithNamePayload JSON encoding error: %s", err)
					http.Error(
						r.w,
						http.StatusText(http.StatusInternalServerError),
						http.StatusInternalServerError,
					)
					return
				}
				return
			default:
				http.Error(
					r.w,
					http.StatusText(http.StatusNotFound),
					http.StatusNotFound,
				)
				return
			}
		default:
			http.Error(
				r.w,
				http.StatusText(http.StatusNotFound),
				http.StatusNotFound,
			)
			return
		}

	default:
		http.Error(
			r.w,
			http.StatusText(http.StatusMethodNotAllowed),
			http.StatusMethodNotAllowed,
		)
		return
	}
}

func main() {

	// Handle flags.
	configFile := flag.String("config", "", "configuration file")
	prodFlag := flag.Bool("prod", false, "enable Let's Encrypt manager and bind TLS port")
	statsFlag := flag.Bool("stats", false, "enable stats listener where expvar data is available")
	flag.Parse()

	// Make sure the returned last-modified field is in GMT time to match
	// Last-Modified header when downloading zip file.
	timeLoc, err := time.LoadLocation("GMT")
	if err != nil {
		log.Fatal(err)
	}

	// Fetch configuration settings.
	config := readConfig(configFile)

	if config.API.Path == "" {
		// An empty Path will panic the server, convert it to "/":
		// panic: http: invalid pattern
		config.API.Path = "/"
	}

	if config.API.Path != "/" {
		if strings.HasSuffix(config.API.Path, "/") {
			// Strip a trailing "/" to make later offset calculations easier
			config.API.Path = strings.TrimSuffix(config.API.Path, "/")
		}
	}

	// When API Path is "/" we need to exclude it in comparisons and
	// logs since otherwise that becomes "//sites" etc.
	basePath := ""
	if config.API.Path != "/" {
		basePath = config.API.Path
	}

	// Build address string.
	connStr := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		config.Database.Host,
		config.Database.Port,
		config.Database.User,
		config.Database.Password,
		config.Database.DBName,
		config.Database.SSLMode,
	)

	db, err := openDB(connStr)
	if err != nil {
		log.Fatal(err)
	}

	// Use a custom ServeMux so things like expvar does not interfere with
	// the publicly exposed service
	mux := http.NewServeMux()

	mux.HandleFunc(config.API.Path, handlerWrapper(tlAPIHandler, db, config, timeLoc, basePath))
	if config.API.Path != "/" {
		// Make it so we handle requests both with and without a trailing "/"
		// on the API path, since we want to match requests for
		// subtrees like /rank and /site as well as the plain API path.
		mux.HandleFunc(config.API.Path+"/", handlerWrapper(tlAPIHandler, db, config, timeLoc, basePath))
	}

	// For a several layers deep API path like "/api/v1" or more we want to respond with
	// a "Bad Request" pointing to existing paths rather than "Not Found"
	// to a query for any path above that point
	//if len(strings.Split(config.API.Path, "/")) > 2 {
	if config.API.Path != "/" {
		mux.HandleFunc("/", func(basePath string) func(w http.ResponseWriter, req *http.Request) {
			return func(w http.ResponseWriter, req *http.Request) {
				badRequestHint(w, basePath)
			}
		}(basePath))
	}

	rl := newRateLimit(config.Server.RateLimit, config.Server.BurstLimit)

	var acm *autocert.Manager

	// Handle Let's Encrypt certificates automatically
	if *prodFlag {
		acm = &autocert.Manager{
			Cache:      autocert.DirCache("autocert-dir"),
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(config.Server.HostWhitelist...),
		}
	}

	srv := http.Server{
		Handler:      muxWrapper(mux, rl),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  10 * time.Second,
	}

	if *prodFlag {
		srv.TLSConfig = acm.TLSConfig()
		srv.Addr = fmt.Sprintf("%s:%d", config.Server.Address, config.Server.Port)
	} else {
		srv.Addr = fmt.Sprintf("%s:%d", config.Server.DevAddress, config.Server.DevPort)
	}

	// Make server shut down on receipt of termination signals.
	idleConnsClosed := make(chan struct{})
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan

		// We received an interrupt signal, shut down.
		if err := srv.Shutdown(context.Background()); err != nil {
			// Error from closing listeners, or context timeout:
			log.Printf("HTTP server Shutdown: %v", err)
		}
		close(idleConnsClosed)
	}()

	// Parse time values for rate limit cleanup code.
	cleanupIntervalDuration, err := time.ParseDuration(config.Server.LimitCleanupInterval)
	if err != nil {
		log.Fatalf("unable to parse limitcleanupinterval: %s", config.Server.LimitCleanupInterval)
	}
	cleanupAgeDuration, err := time.ParseDuration(config.Server.LimitCleanupAge)
	if err != nil {
		log.Fatalf("unable to parse limitcleanupage: %s", config.Server.LimitCleanupAge)
	}

	// Start rate limit cleanup function in the background.
	go rateLimitCleanup(rl, cleanupIntervalDuration, cleanupAgeDuration)

	// Add runtime route to the stats listener
	http.HandleFunc("/runtime", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s\n", runtime.Version())
	})

	// Start stats listener where expvar will make /debug/vars available
	if *statsFlag {
		go http.ListenAndServe(fmt.Sprintf("%s:%d", config.Server.StatsAddress, config.Server.StatsPort), nil)
	}

	if *prodFlag {
		// Enable Let's Encrypt http-01 listener
		go http.ListenAndServe(":80", acm.HTTPHandler(nil))
	}

	// Start listening.
	if *prodFlag {
		if err := srv.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
			// Error starting or closing listener:
			log.Fatalf("HTTP server ListendAndServeTLS: %v", err)
		}
	} else {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			// Error starting or closing listener:
			log.Fatalf("HTTP server ListendAndServeTLS: %v", err)
		}
	}
	// ListenAndServe() returns immeduately when Shutdown() is called, wait
	// here for Shutdown() to complete gracefully.
	log.Println("receieved signal, waiting for any outstanding requests to finish...")
	<-idleConnsClosed
	log.Println("server exiting.")
}
