package tlapi

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/lib/pq"
)

// openDB creates the DB connection.
func openDB(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	return db, nil
}

// pingDB verifies the conncetion to the database.
func pingDB(db *sql.DB) error {
	err := db.Ping()
	if err != nil {
		return err
	}

	return nil
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

// vacuumAnalyze runs every time we have updated the database with a new list
func vacuumAnalyze(db *sql.DB, table string) error {
	// https://www.postgresql.org/docs/current/sql-vacuum.html:
	//
	// After adding or deleting a large number of rows, it might be a good
	// idea to issue a VACUUM ANALYZE command for the affected table. This
	// will update the system catalogs with the results of all recent
	// changes, and allow the PostgreSQL query planner to make better
	// choices in planning queries.

	// QuoteIdentifier quotes an "identifier" (e.g. a table or a column
	// name) to be used as part of an SQL statement.
	quotedTable := pq.QuoteIdentifier(table)

	_, err := db.Exec(fmt.Sprintf("VACUUM ANALYZE %s", quotedTable))
	if err != nil {
		return fmt.Errorf("vacuumAnalyze %s: %s", quotedTable, err)
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

	return []siteEntry{{Rank: rank, Site: site}}, nil
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

	return []siteEntry{{Rank: rank, Site: site}}, nil
}

func repeatableReadTransaction(db *sql.DB) (*sql.Tx, error) {
	// Make sure we have a consistent view of the database between SELECTs in
	// case the database is updated under our feet.

	// https://www.postgresql.org/docs/current/transaction-iso.html:
	//
	// The Repeatable Read isolation level only sees data committed before
	// the transaction began; it never sees either uncommitted data or
	// changes committed during transaction execution by concurrent
	// transactions. (However, the query does see the effects of previous
	// updates executed within its own transaction, even though they are
	// not yet committed.) This is a stronger guarantee than is required by
	// the SQL standard for this isolation level, and prevents all of the
	// phenomena described in Table 13.1 except for serialization
	// anomalies. As mentioned above, this is specifically allowed by the
	// standard, which only describes the minimum protections each
	// isolation level must provide.
	// [...]
	// Note that only updating transactions might need to be retried;
	// read-only transactions will never have serialization conflicts.
	tx, err := db.BeginTx(
		context.Background(),
		&sql.TxOptions{
			Isolation: sql.LevelRepeatableRead,
			ReadOnly:  true,
		},
	)
	if err != nil {
		return nil, err
	}
	return tx, nil
}
