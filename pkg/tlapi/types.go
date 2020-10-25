package tlapi

import (
	"database/sql"
	"golang.org/x/time/rate"
	"net/http"
	"time"
)

// apiServiceConfig holds information read from the config file used by the API service.
type apiServiceConfig struct {
	Server      apiServerConfig
	Database    databaseConfig
	API         apiConfig
	CertCacheDB databaseConfig
}

// serverConfig contains settings regarding the running server.
type apiServerConfig struct {
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
	HTTPChallengePort    int
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

// DatabaseConfig contains settings for the database connection.
type databaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
}

// request is the struct that is passed to our API handler function, it allows us to
// pass anything we need without depending on global variables.
type request struct {
	w        http.ResponseWriter
	req      *http.Request
	db       *sql.DB
	config   *apiServiceConfig
	timeLoc  *time.Location
	basePath string
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

// updaterData holds information passed to the dbwriter updater loop.
type updaterData struct {
	config   *dbWriterConfig
	db       *sql.DB
	hc       *http.Client
	chanErr  chan error
	interval time.Duration
}

// dbWriterConfig holds information read from the dbwriter config file.
type dbWriterConfig struct {
	Database databaseConfig
	Updater  updaterConfig
}

// updaterConfig contains settings for the main dbwriter updater loop
type updaterConfig struct {
	Interval  string
	JitterMin int
	JitterMax int
}
