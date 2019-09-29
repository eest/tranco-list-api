package tlapi

import (
	"database/sql"
)

// OpenDB creates the DB connection.
func OpenDB(connStr string) (*sql.DB, error) {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	return db, nil
}

// PingDB verifies the conncetion to the database.
func PingDB(db *sql.DB) error {
	err := db.Ping()
	if err != nil {
		return err
	}

	return nil
}
