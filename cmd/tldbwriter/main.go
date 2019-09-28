package main

import (
	"archive/zip"
	"bufio"
	crand "crypto/rand"
	"database/sql"
	"encoding/binary"
	"flag"
	"fmt"
	"github.com/BurntSushi/toml"
	"github.com/lib/pq"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
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
	// serviceName is the general name of this service that is the default name
	// used for databases and such.
	serviceName = "trancolist"
)

// updaterData holds information passed to the updater loop.
type updaterData struct {
	config   *mainConfig
	db       *sql.DB
	hc       *http.Client
	chanErr  chan error
	interval time.Duration
}

// mainConfig holds information read from the config file.
type mainConfig struct {
	Database databaseConfig
	Updater  updaterConfig
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

// updaterConfig contains settings for the main updater loop
type updaterConfig struct {
	Interval  string
	JitterMin int
	JitterMax int
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
		Database: databaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     serviceName,
			Password: "",
			DBName:   serviceName,
			SSLMode:  "verify-full",
		},
		Updater: updaterConfig{
			Interval:  "1h",
			JitterMin: 0,
			JitterMax: 300,
		},
	}
}

// initDB runs every time we attempt to update the database to makes sure the
// tables are available.
func initDB(tx *sql.Tx) error {
	var err error

	_, err = tx.Exec(
		`CREATE TABLE IF NOT EXISTS lists (
                    id BIGSERIAL PRIMARY KEY,
                    name text UNIQUE NOT NULL,
                    ts timestamptz NOT NULL
                )`,
	)
	if err != nil {
		return fmt.Errorf("create lists: %s", err)
	}

	_, err = tx.Exec(
		`CREATE TABLE IF NOT EXISTS sites (
                id BIGSERIAL PRIMARY KEY,
                list_id BIGINT NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
                rank BIGINT NOT NULL,
                site TEXT NOT NULL,
                UNIQUE (list_id, rank),
                UNIQUE (list_id, site)
            )`,
	)
	if err != nil {
		return fmt.Errorf("create sites: %s", err)
	}

	return nil
}

// fetchFile attempts to download the zip file containing the top list
func fetchFile(hc *http.Client, ulID string, tmpfile *os.File) (time.Time, error) {

	log.Printf("downloading %s to %s", ulID, tmpfile.Name())

	resp, err := hc.Get(fmt.Sprintf("https://tranco-list.eu/download_daily/%s", ulID))
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()

	// https://tranco-list.eu/: the Last-Modified header provides an exact
	// timestamp
	// Last-Modified: Wed, 28 Aug 2019 22:15:44 GMT
	// time.RFC1123 = "Mon, 02 Jan 2006 15:04:05 MST"
	t, err := time.Parse(time.RFC1123, resp.Header.Get("Last-Modified"))
	if err != nil {
		return time.Time{}, err
	}

	// Write output directly to file to not fill up RAM unnecessarily
	_, err = io.Copy(tmpfile, resp.Body)
	if err != nil {
		return time.Time{}, err
	}

	return t, nil
}

// getListID fetches the DB id given the name of the list
func getListID(tx *sql.Tx, listName string) (int64, error) {

	var id int64
	err := tx.QueryRow("SELECT id FROM lists WHERE name = $1", listName).Scan(&id)
	switch {
	case err == sql.ErrNoRows:
		// Not an error, the list will be inserted
		return id, nil
	case err != nil:
		return 0, fmt.Errorf("getListID: %s", err.Error())
	}

	return id, nil
}

// insertList adds a list to the database and returns the newly created DB id
func insertList(tx *sql.Tx, listName string, t time.Time) (int64, error) {

	var id int64
	err := tx.QueryRow("INSERT INTO lists (name, ts) VALUES ($1, $2) RETURNING id", listName, t).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insertList: %s failed: %s", listName, err.Error())
	}

	return id, nil
}

// getOldLists returns a list of listnames that can be removed given the
// number of lists to keep.
func getOldLists(tx *sql.Tx, keep int) ([]string, error) {

	var name string
	var names []string

	rows, err := tx.Query(`SELECT name FROM lists ORDER BY ts DESC`)
	if err != nil {
		return []string{}, fmt.Errorf("getOldLists SELECT failed: %s", err)
	}
	defer rows.Close()

	for rows.Next() {
		err = rows.Scan(&name)
		if err != nil {
			return []string{}, fmt.Errorf("getOldLists Scan failed: %s", err)
		}

		names = append(names, name)
	}

	// If the available number of lists are less or equal to the number we
	// want to keep return an empty slice so nothing is removed.
	if len(names) <= keep {
		return []string{}, nil
	}

	return names[keep:], nil
}

// deleteListNames removes the supplied list of names from the DB
func deleteListNames(tx *sql.Tx, listNames []string) error {

	for _, listName := range listNames {
		log.Printf("deleting list with name %s", listName)
		_, err := deleteListName(tx, listName)
		if err != nil {
			return err
		}
	}

	return nil
}

// deleteListName removes the given list name from the DB
func deleteListName(tx *sql.Tx, listName string) (int64, error) {

	var id int64
	err := tx.QueryRow("DELETE FROM lists WHERE name = $1 RETURNING id", listName).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("deleteList: %s failed: %s", listName, err.Error())
	}

	return id, nil
}

// getSiteID returns the DB id for a site
func getSiteID(tx *sql.Tx, listID int64, rank int64, site string) (int64, error) {

	var id int64
	err := tx.QueryRow("SELECT id FROM sites WHERE list_id = $1 AND rank = $2 AND site = $3", listID, rank, site).Scan(&id)
	switch {
	case err == sql.ErrNoRows:
		// Not an error, the site will be inserted
		return id, nil
	case err != nil:
		return 0, fmt.Errorf("getSiteID: %s", err.Error())
	}

	return id, nil
}

// insertSite adds a site to the DB
func insertSite(tx *sql.Tx, listID int64, rank int64, site string) (int64, error) {

	var id int64
	err := tx.QueryRow("INSERT INTO sites (list_id, rank, site) VALUES ($1, $2, $3) RETURNING id", listID, rank, site).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insertSite: %s failed: %s", site, err.Error())
	}

	return id, nil
}

// loadZip reads the downloaded zip file and inserts the contents into the DB
func loadZip(ulID string, tmpfile *os.File, tx *sql.Tx, t time.Time) error {

	// Open a zip archive for reading.
	tfStat, err := tmpfile.Stat()
	if err != nil {
		return err
	}

	log.Printf("reading zip file %s with size %d bytes", tmpfile.Name(), tfStat.Size())

	r, err := zip.NewReader(tmpfile, tfStat.Size())
	if err != nil {
		return err
	}

	// Iterate through the files in the archive,
	// printing some of their contents.
	for _, f := range r.File {
		if f.Name != "top-1m.csv" {
			log.Printf("skipping unknown file: %s", f.Name)
			continue
		}

		log.Printf("importing list with ID %s from zipped file %s", ulID, f.Name)
		listID, err := insertList(tx, ulID, t)
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close() // #nosec

		scanner := bufio.NewScanner(rc)

		// When bulk inserting records it is significantly faster to
		// use pq.CopyIn (which uses COPY FROM internally) instead of
		// using INSERT.
		// See https://godoc.org/github.com/lib/pq#hdr-Bulk_imports
		stmt, err := tx.Prepare(pq.CopyIn("sites", "list_id", "rank", "site"))
		if err != nil {
			return err
		}
		for scanner.Scan() {
			parts := strings.Split(scanner.Text(), ",")
			if len(parts) != 2 {
				return fmt.Errorf("unexpected number of fields in input text: %s", scanner.Text())
			}
			rankInt, err := strconv.Atoi(parts[0])
			if err != nil {
				return err
			}
			site := parts[1]
			rank := int64(rankInt)

			_, err = stmt.Exec(listID, rank, site)
			if err != nil {
				return err
			}

		}

		// https://godoc.org/github.com/lib/pq#hdr-Bulk_imports:
		//  After all data has been processed you should call Exec()
		//  once with no arguments to flush all buffered data.
		_, err = stmt.Exec()
		if err != nil {
			return err
		}

		err = stmt.Close()
		if err != nil {
			return err
		}
		if err := scanner.Err(); err != nil {
			return err
		}
	}

	return nil
}

// openTx creates a transaction for later use and makes sure the database is initialized
func openTx(db *sql.DB) (*sql.Tx, error) {

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}

	err = initDB(tx)
	if err != nil {
		tx.Rollback() // #nosec
		return nil, err
	}

	return tx, nil
}

// upstreamListID looks up the latest available list ID
func upstreamListID(hc *http.Client) (string, error) {
	resp, err := hc.Get("https://tranco-list.eu/top-1m-id")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// listCleanup makes sure we remove old lists from the database after a new one
// has been added to not create an ever increasing storage footprint
func listCleanup(tx *sql.Tx) error {

	// How many lists to keep
	numKeep := 1

	listNames, err := getOldLists(tx, numKeep)
	if err != nil {
		return err
	}

	err = deleteListNames(tx, listNames)
	if err != nil {
		return err
	}

	return nil
}

// updateDatabase is the function doing all work related to fetching a new list
// and loading it into the database
func updateDatabase(tx *sql.Tx, hc *http.Client, ulID string) error {
	tmpfile, err := ioutil.TempFile("", fmt.Sprintf("tranco-list_%s_*.zip", ulID))
	if err != nil {
		return err
	}
	defer tmpfile.Close()
	defer os.Remove(tmpfile.Name()) // clean up

	fetchStart := time.Now()
	t, err := fetchFile(hc, ulID, tmpfile)
	if err != nil {
		return err
	}
	fetchDuration := time.Since(fetchStart)

	loadStart := time.Now()
	err = loadZip(ulID, tmpfile, tx, t)
	if err != nil {
		return err
	}
	loadDuration := time.Since(loadStart)

	cleanupStart := time.Now()
	err = listCleanup(tx)
	if err != nil {
		return err
	}
	cleanupDuration := time.Since(cleanupStart)

	commitStart := time.Now()
	err = tx.Commit()
	if err != nil {
		return err
	}
	commitDuration := time.Since(commitStart)

	log.Printf(
		"%s commited to database, fetch: %s, load: %s, cleanup: %s, commit: %s",
		ulID,
		fetchDuration,
		loadDuration,
		cleanupDuration,
		commitDuration,
	)

	return nil
}

// checkForUpdates checks the latest upstream list against content in the DB
// and executes an update if necessary
func checkForUpdates(db *sql.DB, hc *http.Client) error {
	log.Println("checking for updates")
	ulID, err := upstreamListID(hc)
	if err != nil {
		return err
	}

	log.Println("creating transaction")
	tx, err := openTx(db)
	if err != nil {
		return err
	}

	log.Println("checking local ID")
	listID, err := getListID(tx, ulID)
	if err != nil {
		return err
	}

	if listID == 0 {
		err = updateDatabase(tx, hc, ulID)
		if err != nil {
			return err
		}
	} else {
		log.Printf("list %s already present in database, nothing to do", ulID)
		err = tx.Rollback()
		if err != nil {
			return err
		}
	}

	return nil
}

// openDB creates the DB connection
func openDB(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	return db, nil
}

// runUpdater is the main loop continually trying to updating the database
func runUpdater(ud updaterData) {
	var err error

	for {
		err = checkForUpdates(ud.db, ud.hc)
		if err != nil {
			ud.chanErr <- err
			return
		}

		// Get a random number of seconds between JitterMin and JitterMax (inclusive).
		jitter := time.Duration(
			ud.config.Updater.JitterMin+rand.Intn(
				ud.config.Updater.JitterMax-ud.config.Updater.JitterMin+1,
			),
		) * time.Second

		sleepDuration := ud.interval + jitter
		log.Printf("time until next check: %s", sleepDuration.String())
		time.Sleep(sleepDuration)
	}
}

// getRandomSeed returns a seed based on cryptographically secure pseudorandom numbers
func getRandomSeed() (int64, error) {

	// Mix of https://godoc.org/crypto/rand and
	// https://stackoverflow.com/questions/12321133/golang-random-number-generator-how-to-seed-properly
	c := 10
	b := make([]byte, c)
	_, err := crand.Read(b)
	if err != nil {
		return 0, err
	}

	return int64(binary.LittleEndian.Uint64(b)), nil

}

func main() {
	log.Printf("go runtime version: %s", runtime.Version())

	// Handle flags.
	configFile := flag.String("config", "", "configuration file")
	flag.Parse()

	// Fetch configuration settings.
	config := readConfig(configFile)

	// Seed the PRNG with some unknown data to be able to add some random
	// jitter to the sleep duration in the main loop.
	seed, err := getRandomSeed()
	if err != nil {
		log.Fatal(err)
	}
	rand.Seed(seed)

	interval, err := time.ParseDuration(config.Updater.Interval)
	if err != nil {
		log.Fatal(err)
	}

	// Build database string.
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

	// listen for signals so we can shut down nicely
	sc := make(chan os.Signal)
	signal.Notify(sc, os.Interrupt, syscall.SIGTERM)

	// ce is used for reading errors from the updater loop
	ce := make(chan error)

	// Make sure we do not hang forever in HTTP connections. For good
	// timeout values look at the 'fetch' duration for a successfull request:
	// 2019/09/01 08:41:36 93P2 commited to database, fetch: 1.669124619s [...]
	hc := &http.Client{
		Timeout: 10 * time.Second,
	}

	ud := updaterData{
		config:   config,
		db:       db,
		hc:       hc,
		chanErr:  ce,
		interval: interval,
	}

	go runUpdater(ud)

	select {
	case err := <-ce:
		log.Fatal(err)
	case sig := <-sc:
		log.Printf("received signal \"%s\", exiting", sig)
	}
}
