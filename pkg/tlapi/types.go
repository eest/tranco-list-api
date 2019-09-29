package tlapi

// DatabaseConfig contains settings for the database connection.
type DatabaseConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
}
