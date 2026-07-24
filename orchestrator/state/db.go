package state

import (
	"database/sql"
	"fmt"
	"log"
	"sync"

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
	mu   sync.Mutex
}

// InitDB initializes the SQLite database for state management.
func InitDB(dbPath string) (*DB, error) {
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
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
		file_path TEXT NOT NULL,
		track_number INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		error_message TEXT,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (file_path, track_number)
	);
	`
	_, err := db.conn.Exec(query)
	if err != nil {
		return fmt.Errorf("failed to create task_state table: %w", err)
	}

	return db.migrateTables()
}

func (db *DB) migrateTables() error {
	rows, err := db.conn.Query(`PRAGMA table_info(task_state);`)
	if err != nil {
		return nil
	}
	hasTrackNumber := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dfltValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err == nil {
			if name == "track_number" {
				hasTrackNumber = true
				break
			}
		}
	}
	rows.Close()

	if !hasTrackNumber {
		migrationQuery := `
		CREATE TABLE IF NOT EXISTS task_state_new (
			file_path TEXT NOT NULL,
			track_number INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL,
			error_message TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (file_path, track_number)
		);
		INSERT OR IGNORE INTO task_state_new (file_path, track_number, status, error_message, updated_at)
			SELECT file_path, 0, status, error_message, updated_at FROM task_state;
		DROP TABLE task_state;
		ALTER TABLE task_state_new RENAME TO task_state;
		`
		_, err := db.conn.Exec(migrationQuery)
		if err != nil {
			log.Printf("Warning: failed to migrate task_state table: %v", err)
		} else {
			log.Println("Successfully migrated orchestrator.db task_state to composite primary key (file_path, track_number)")
		}
	}
	return nil
}

// ResetStaleTasks resets any RUNNING or PENDING tasks to FAILED upon orchestrator startup.
func (db *DB) ResetStaleTasks() (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

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
	return db.CheckOrInsertWithForce(filePath, 0, false)
}

// CheckOrInsertWithForce checks if a task should be executed, supporting track_number and a force override flag.
func (db *DB) CheckOrInsertWithForce(filePath string, trackNumber int, force bool) (bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	tx, err := db.conn.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var status string
	err = tx.QueryRow(`SELECT status FROM task_state WHERE file_path = ? AND track_number = ?`, filePath, trackNumber).Scan(&status)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	if err == nil {
		// Found existing record
		if force || status == string(StatusFailed) {
			_, err = tx.Exec(`UPDATE task_state SET status = ?, error_message = NULL, updated_at = CURRENT_TIMESTAMP WHERE file_path = ? AND track_number = ?`, StatusPending, filePath, trackNumber)
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
	_, err = tx.Exec(`INSERT INTO task_state (file_path, track_number, status) VALUES (?, ?, ?)`, filePath, trackNumber, StatusPending)
	if err != nil {
		return false, err
	}

	return true, tx.Commit()
}

// UpdateStatus updates the status of a task.
func (db *DB) UpdateStatus(filePath string, trackNumber int, status TaskStatus, errMsg string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	_, err := db.conn.Exec(`
		UPDATE task_state 
		SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP 
		WHERE file_path = ? AND track_number = ?
	`, status, errMsg, filePath, trackNumber)
	return err
}

func (db *DB) Close() error {
	return db.conn.Close()
}

