package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// BandwidthRecord represents a single bandwidth measurement
type BandwidthRecord struct {
	ID            int64
	ServerName    string
	Timestamp     time.Time
	RxBytes       uint64
	TxBytes       uint64
	RxBytesPerSec uint64
	TxBytesPerSec uint64
}

// BandwidthDB handles SQLite storage for bandwidth data
type BandwidthDB struct {
	db *sql.DB
}

// NewBandwidthDB creates a new bandwidth database connection
func NewBandwidthDB(dbPath string) (*BandwidthDB, error) {
	// Create directory if it doesn't exist
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Enable WAL mode for better concurrent access
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}

	bdb := &BandwidthDB{db: db}
	if err := bdb.initSchema(); err != nil {
		return nil, fmt.Errorf("initializing schema: %w", err)
	}

	return bdb, nil
}

// initSchema creates the database schema
func (bdb *BandwidthDB) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS bandwidth_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		server_name TEXT NOT NULL,
		timestamp DATETIME NOT NULL,
		rx_bytes INTEGER NOT NULL,
		tx_bytes INTEGER NOT NULL,
		rx_bytes_per_sec INTEGER NOT NULL,
		tx_bytes_per_sec INTEGER NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_bandwidth_server_time 
		ON bandwidth_history(server_name, timestamp DESC);
	
	CREATE INDEX IF NOT EXISTS idx_bandwidth_timestamp 
		ON bandwidth_history(timestamp DESC);
	`

	if _, err := bdb.db.Exec(schema); err != nil {
		return fmt.Errorf("creating schema: %w", err)
	}

	return nil
}

// Insert adds a new bandwidth record
func (bdb *BandwidthDB) Insert(record BandwidthRecord) error {
	query := `
		INSERT INTO bandwidth_history 
		(server_name, timestamp, rx_bytes, tx_bytes, rx_bytes_per_sec, tx_bytes_per_sec)
		VALUES (?, ?, ?, ?, ?, ?)
	`

	_, err := bdb.db.Exec(query,
		record.ServerName,
		record.Timestamp,
		record.RxBytes,
		record.TxBytes,
		record.RxBytesPerSec,
		record.TxBytesPerSec,
	)

	if err != nil {
		return fmt.Errorf("inserting record: %w", err)
	}

	return nil
}

// GetLatest returns the most recent bandwidth record for a server
func (bdb *BandwidthDB) GetLatest(serverName string) (*BandwidthRecord, error) {
	query := `
		SELECT id, server_name, timestamp, rx_bytes, tx_bytes, rx_bytes_per_sec, tx_bytes_per_sec
		FROM bandwidth_history
		WHERE server_name = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`

	var record BandwidthRecord
	err := bdb.db.QueryRow(query, serverName).Scan(
		&record.ID,
		&record.ServerName,
		&record.Timestamp,
		&record.RxBytes,
		&record.TxBytes,
		&record.RxBytesPerSec,
		&record.TxBytesPerSec,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("querying latest record: %w", err)
	}

	return &record, nil
}

// GetHistory returns bandwidth records for a server within a time range
func (bdb *BandwidthDB) GetHistory(serverName string, since time.Time, limit int) ([]BandwidthRecord, error) {
	query := `
		SELECT id, server_name, timestamp, rx_bytes, tx_bytes, rx_bytes_per_sec, tx_bytes_per_sec
		FROM bandwidth_history
		WHERE server_name = ? AND timestamp >= ?
		ORDER BY timestamp DESC
		LIMIT ?
	`

	rows, err := bdb.db.Query(query, serverName, since, limit)
	if err != nil {
		return nil, fmt.Errorf("querying history: %w", err)
	}
	defer rows.Close()

	var records []BandwidthRecord
	for rows.Next() {
		var record BandwidthRecord
		if err := rows.Scan(
			&record.ID,
			&record.ServerName,
			&record.Timestamp,
			&record.RxBytes,
			&record.TxBytes,
			&record.RxBytesPerSec,
			&record.TxBytesPerSec,
		); err != nil {
			return nil, fmt.Errorf("scanning row: %w", err)
		}
		records = append(records, record)
	}

	return records, nil
}

// GetAllServersLatest returns the latest bandwidth for all servers
func (bdb *BandwidthDB) GetAllServersLatest() (map[string]BandwidthRecord, error) {
	query := `
		SELECT DISTINCT server_name FROM bandwidth_history
	`

	rows, err := bdb.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("querying servers: %w", err)
	}
	defer rows.Close()

	result := make(map[string]BandwidthRecord)
	var servers []string

	for rows.Next() {
		var serverName string
		if err := rows.Scan(&serverName); err != nil {
			return nil, fmt.Errorf("scanning server name: %w", err)
		}
		servers = append(servers, serverName)
	}

	// Get latest for each server
	for _, serverName := range servers {
		record, err := bdb.GetLatest(serverName)
		if err != nil {
			return nil, err
		}
		if record != nil {
			result[serverName] = *record
		}
	}

	return result, nil
}

// CleanupOld removes records older than the specified duration
func (bdb *BandwidthDB) CleanupOld(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)

	query := `DELETE FROM bandwidth_history WHERE timestamp < ?`
	result, err := bdb.db.Exec(query, cutoff)
	if err != nil {
		return 0, fmt.Errorf("cleaning up old records: %w", err)
	}

	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting rows affected: %w", err)
	}

	return count, nil
}

// GetStats returns statistics about stored bandwidth data
func (bdb *BandwidthDB) GetStats() (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Total records
	var totalRecords int64
	err := bdb.db.QueryRow("SELECT COUNT(*) FROM bandwidth_history").Scan(&totalRecords)
	if err != nil {
		return nil, fmt.Errorf("counting records: %w", err)
	}
	stats["total_records"] = totalRecords

	// Number of servers
	var serverCount int64
	err = bdb.db.QueryRow("SELECT COUNT(DISTINCT server_name) FROM bandwidth_history").Scan(&serverCount)
	if err != nil {
		return nil, fmt.Errorf("counting servers: %w", err)
	}
	stats["server_count"] = serverCount

	// Oldest record
	var oldest time.Time
	err = bdb.db.QueryRow("SELECT MIN(timestamp) FROM bandwidth_history").Scan(&oldest)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("querying oldest: %w", err)
	}
	stats["oldest_record"] = oldest

	// Newest record
	var newest time.Time
	err = bdb.db.QueryRow("SELECT MAX(timestamp) FROM bandwidth_history").Scan(&newest)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("querying newest: %w", err)
	}
	stats["newest_record"] = newest

	return stats, nil
}

// Close closes the database connection
func (bdb *BandwidthDB) Close() error {
	return bdb.db.Close()
}
