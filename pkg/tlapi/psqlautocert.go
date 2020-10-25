package tlapi

import (
	"context"
	"database/sql"
	"fmt"
	"golang.org/x/crypto/acme/autocert"
	"log"
	"time"
)

type psqlCertCache struct {
	db *sql.DB
}

func newPsqlCertCache(config *apiServiceConfig) (*psqlCertCache, error) {
	connStr := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		config.CertCacheDB.Host,
		config.CertCacheDB.Port,
		config.CertCacheDB.User,
		config.CertCacheDB.Password,
		config.CertCacheDB.DBName,
		config.CertCacheDB.SSLMode,
	)

	db, err := openDB(connStr)
	if err != nil {
		return nil, err
	}

	err = initCertCacheDB(db)
	if err != nil {
		return nil, err
	}

	return &psqlCertCache{db: db}, nil
}

func initCertCacheDB(db *sql.DB) error {
	var err error

	log.Printf("initializing psql autocert cache")

	_, err = db.Exec(
		`CREATE TABLE IF NOT EXISTS certcache (
                    ts timestamptz NOT NULL,
                    key text UNIQUE NOT NULL,
                    data bytea NOT NULL
                )`,
	)
	if err != nil {
		return fmt.Errorf("create psql certcache: %s", err)
	}

	return nil
}

func (pcc *psqlCertCache) Get(ctx context.Context, key string) ([]byte, error) {
	var data []byte

	log.Printf("getting certcache key %s", key)

	err := pcc.db.QueryRowContext(ctx, "SELECT data FROM certcache WHERE key = $1", key).Scan(&data)
	switch {
	case err == sql.ErrNoRows:
		return []byte{}, autocert.ErrCacheMiss
	case err != nil:
		return []byte{}, fmt.Errorf("pcc Get: %s", err.Error())
	}

	return data, nil
}

func (pcc *psqlCertCache) Put(ctx context.Context, key string, data []byte) error {

	log.Printf("putting certcache key %s", key)

	result, err := pcc.db.ExecContext(ctx, "INSERT INTO certcache (ts, key, data) VALUES ($1, $2, $3)", time.Now(), key, data)
	if err != nil {
		return fmt.Errorf("pcc Put: INSERT %s failed: %s", key, err.Error())
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("pcc Put: RowsAffected error: %s", err)
	}

	if rows != 1 {
		return fmt.Errorf("pcc Put: expected to affect 1 row, affected %d", rows)
	}

	return nil
}

func (pcc *psqlCertCache) Delete(ctx context.Context, key string) error {

	log.Printf("deleting certcache key %s", key)

	result, err := pcc.db.ExecContext(ctx, "DELETE FROM certcache WHERE key = $1", key)
	if err != nil {
		return fmt.Errorf("pcc Delete: DELETE %s failed: %s", key, err.Error())
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("pcc Put: RowsAffected error: %s", err)
	}

	if rows != 1 {
		return fmt.Errorf("pcc Put: expected to affect 1 row, affected %d", rows)
	}

	return nil
}
