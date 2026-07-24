package state

import (
	"database/sql"
	"fmt"
	"log"

	_ "modernc.org/sqlite"
)

type TaskStatus string

const (
	StatusPending   TaskStatus = "PENDING"
	StatusRunning   TaskStatus = "RUNNING"
	StatusCompleted TaskStatus = "COMPLETED"
	StatusFailed    TaskStatus = "FAILED"
)

type DB struct {
	conn *sql.DB
}

// InitDB initializes the SQLite database for state management.
func InitDB(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	// Enable WAL mode for high concurrency
	_, err = conn.Exec(`PRAGMA journal_mode=WAL;`)
	if err != nil {
		log.Printf("Warning: failed to set WAL mode: %v", err)
	}
	_, err = conn.Exec(`PRAGMA synchronous=NORMAL;`)
	if err != nil {
		log.Printf("Warning: failed to set synchronous mode: %v", err)
	}

	db := &DB{conn: conn}
	if err := db.createTables(); err != nil {
		return nil, err
	}
	return db, nil
}

func (db *DB) createTables() error {
	query := `
	CREATE TABLE IF NOT EXISTS task_state (
		file_path TEXT PRIMARY KEY,
		status TEXT NOT NULL,
		error_message TEXT,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := db.conn.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to create task_state table: %w", err)
	}
	return nil
}

// ResetStaleTasks resets any RUNNING or PENDING tasks to FAILED upon orchestrator startup.
func (db *DB) ResetStaleTasks() (int64, error) {
	res, err := db.conn.Exec(`
		UPDATE task_state 
		SET status = ?, error_message = 'Interrupted by orchestrator restart', updated_at = CURRENT_TIMESTAMP 
		WHERE status IN (?, ?)
	`, StatusFailed, StatusRunning, StatusPending)
	if err != nil {
		return 0, fmt.Errorf("failed to reset stale tasks: %w", err)
	}
	return res.RowsAffected()
}

// CheckOrInsert checks if a task is already processed or processing.
func (db *DB) CheckOrInsert(filePath string) (bool, error) {
	return db.CheckOrInsertWithForce(filePath, false)
}

// CheckOrInsertWithForce checks if a task should be executed, supporting a force override flag.
func (db *DB) CheckOrInsertWithForce(filePath string, force bool) (bool, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var status string
	err = tx.QueryRow(`SELECT status FROM task_state WHERE file_path = ?`, filePath).Scan(&status)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	if err == nil {
		// Found existing record
		if force || status == string(StatusFailed) {
			_, err = tx.Exec(`UPDATE task_state SET status = ?, error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE file_path = ?`, StatusPending, filePath)
			if err != nil {
				return false, err
			}
			return true, tx.Commit()
		}
		if status == string(StatusCompleted) || status == string(StatusRunning) || status == string(StatusPending) {
			return false, nil
		}
	}

	// Not found, insert new
	_, err = tx.Exec(`INSERT INTO task_state (file_path, status) VALUES (?, ?)`, filePath, StatusPending)
	if err != nil {
		return false, err
	}

	return true, tx.Commit()
}

// UpdateStatus updates the status of a task.
func (db *DB) UpdateStatus(filePath string, status TaskStatus, errMsg string) error {
	_, err := db.conn.Exec(`
		UPDATE task_state 
		SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP 
		WHERE file_path = ?
	`, status, errMsg, filePath)
	return err
}

func (db *DB) Close() error {
	return db.conn.Close()
}

