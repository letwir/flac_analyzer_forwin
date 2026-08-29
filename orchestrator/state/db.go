package state

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type TaskStatus string

const (
	StatusPending          TaskStatus = "PENDING"
	StatusQueued           TaskStatus = "QUEUED"
	StatusRunning          TaskStatus = "RUNNING"
	StatusCompleted        TaskStatus = "COMPLETED"
	StatusFailed           TaskStatus = "FAILED"
	StatusFailedMaybeRetry TaskStatus = "FAILED_MAYBE_RETRY"
)

// QueuedTask is the durable representation consumed by the dispatcher feeder.
// PayloadJSON keeps the complete track-level task so a restart does not need to
// rediscover CUE boundaries or metadata before resuming work.
type QueuedTask struct {
	FilePath    string
	TrackNumber int
	PayloadJSON string
}

type priorityPayload struct {
	FileSize    int64 `json:"fileSize"`
	StartSample int64 `json:"startSample"`
	EndSample   int64 `json:"endSample"`
	SampleRate  int   `json:"sampleRate"`
}

func estimatePriorityScore(payloadJSON string) float64 {
	if payloadJSON == "" {
		return 0
	}
	var payload priorityPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return 0
	}
	sampleRate := payload.SampleRate
	if sampleRate <= 0 {
		sampleRate = 44100
	}
	if payload.EndSample > payload.StartSample {
		return float64(payload.EndSample-payload.StartSample) / float64(sampleRate)
	}
	if payload.FileSize > 0 {
		return float64(payload.FileSize) / 176400.0
	}
	return 0
}

type dbWriteOp struct {
	opType      string // "check_or_insert", "update_status"
	filePath    string
	trackNumber int
	payloadJSON string
	status      TaskStatus
	errMsg      string
	force       bool
	limit       int
	resChan     chan dbWriteResult
}

type dbWriteResult struct {
	shouldRun bool
	tasks     []QueuedTask
	count     int64
	err       error
}

type TaskState struct {
	Status       TaskStatus
	ErrorMessage string
}

type DB struct {
	conn    *sql.DB
	opQueue chan dbWriteOp
}

// InitDB initializes the SQLite database with a single-writer async channel loop.
func InitDB(dbPath string) (*DB, error) {
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", dbPath)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	db := &DB{
		conn:    conn,
		opQueue: make(chan dbWriteOp, 10000),
	}
	if err := db.createTables(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	go db.writerLoop()

	return db, nil
}

// OpenReadOnly opens an existing state database without creating or migrating
// schema. It is used by check-only preflight, where any local mutation would
// violate the command contract.
func OpenReadOnly(dbPath string) (*DB, error) {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite db path: %w", err)
	}
	uriPath := filepath.ToSlash(absPath)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     uriPath,
		RawQuery: "mode=ro&_pragma=busy_timeout(10000)",
	}).String()
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db read-only: %w", err)
	}

	var tableName string
	if err := conn.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'task_state'`).Scan(&tableName); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("validate read-only task_state schema: %w", err)
	}
	return &DB{conn: conn}, nil
}

func (db *DB) writerLoop() {
	for op := range db.opQueue {
		switch op.opType {
		case "check_or_insert":
			shouldRun, err := db.execCheckOrInsert(op.filePath, op.trackNumber, op.payloadJSON, op.force)
			if op.resChan != nil {
				op.resChan <- dbWriteResult{shouldRun: shouldRun, err: err}
			}
		case "update_status":
			err := db.execUpdateStatus(op.filePath, op.trackNumber, op.status, op.errMsg)
			if op.resChan != nil {
				op.resChan <- dbWriteResult{err: err}
			}
		case "claim_pending":
			tasks, err := db.execClaimPending(op.limit)
			if op.resChan != nil {
				op.resChan <- dbWriteResult{tasks: tasks, err: err}
			}
		case "requeue_retryable":
			count, err := db.execRequeueRetryable(op.limit, op.errMsg)
			if op.resChan != nil {
				op.resChan <- dbWriteResult{count: count, err: err}
			}
		case "flush":
			if op.resChan != nil {
				op.resChan <- dbWriteResult{}
			}
		}
	}
}

func (db *DB) createTables() error {
	query := `
	CREATE TABLE IF NOT EXISTS task_state (
		file_path TEXT NOT NULL,
		track_number INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		error_message TEXT,
		payload_json TEXT,
		priority_score REAL NOT NULL DEFAULT 0,
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
	hasPayloadJSON := false
	hasPriorityScore := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dfltValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err == nil {
			if name == "track_number" {
				hasTrackNumber = true
			}
			if name == "payload_json" {
				hasPayloadJSON = true
			}
			if name == "priority_score" {
				hasPriorityScore = true
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
			payload_json TEXT,
			priority_score REAL NOT NULL DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (file_path, track_number)
		);
		INSERT OR IGNORE INTO task_state_new (file_path, track_number, status, error_message, payload_json, priority_score, updated_at)
			SELECT file_path, 0, status, error_message, NULL, 0, updated_at FROM task_state;
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
	if hasTrackNumber && !hasPayloadJSON {
		if _, err := db.conn.Exec(`ALTER TABLE task_state ADD COLUMN payload_json TEXT`); err != nil {
			return fmt.Errorf("failed to add task_state.payload_json: %w", err)
		}
	}
	if hasTrackNumber && !hasPriorityScore {
		if _, err := db.conn.Exec(`ALTER TABLE task_state ADD COLUMN priority_score REAL NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("failed to add task_state.priority_score: %w", err)
		}
	}
	if err := db.backfillPriorityScores(); err != nil {
		return err
	}
	if _, err := db.conn.Exec(`CREATE INDEX IF NOT EXISTS idx_task_state_pending_priority ON task_state(status, priority_score, updated_at)`); err != nil {
		return fmt.Errorf("failed to create pending priority index: %w", err)
	}
	return nil
}

func (db *DB) backfillPriorityScores() error {
	rows, err := db.conn.Query(`SELECT file_path, track_number, payload_json FROM task_state WHERE priority_score = 0 AND payload_json IS NOT NULL AND payload_json <> ''`)
	if err != nil {
		return fmt.Errorf("failed to read task payloads for priority backfill: %w", err)
	}
	defer rows.Close()
	type scoreRow struct {
		filePath    string
		trackNumber int
		score       float64
	}
	var updates []scoreRow
	for rows.Next() {
		var filePath, payload string
		var trackNumber int
		if err := rows.Scan(&filePath, &trackNumber, &payload); err != nil {
			return fmt.Errorf("failed to scan task payload for priority backfill: %w", err)
		}
		if score := estimatePriorityScore(payload); score > 0 {
			updates = append(updates, scoreRow{filePath: filePath, trackNumber: trackNumber, score: score})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate task payloads for priority backfill: %w", err)
	}
	for _, update := range updates {
		if _, err := db.conn.Exec(`UPDATE task_state SET priority_score = ? WHERE file_path = ? AND track_number = ? AND priority_score = 0`, update.score, update.filePath, update.trackNumber); err != nil {
			return fmt.Errorf("failed to backfill priority for %s track %d: %w", update.filePath, update.trackNumber, err)
		}
	}
	return nil
}

// ResetStaleTasks preserves durable PENDING work, resumes QUEUED work, and
// marks interrupted RUNNING work as retryable without discarding its payload.
func (db *DB) ResetStaleTasks() (int64, error) {
	res, err := db.conn.Exec(`
		UPDATE task_state
		SET status = ?, error_message = 'Queued task recovered after orchestrator restart', updated_at = CURRENT_TIMESTAMP
		WHERE status = ?
	`, StatusPending, StatusQueued)
	if err != nil {
		return 0, fmt.Errorf("failed to reset stale tasks: %w", err)
	}
	queuedReset, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to count recovered queued tasks: %w", err)
	}

	res, err = db.conn.Exec(`
		UPDATE task_state
		SET status = ?, error_message = 'Interrupted by orchestrator restart; retry is possible', updated_at = CURRENT_TIMESTAMP
		WHERE status = ?
	`, StatusFailedMaybeRetry, StatusRunning)
	if err != nil {
		return 0, fmt.Errorf("failed to mark interrupted running tasks: %w", err)
	}
	runningReset, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to count interrupted running tasks: %w", err)
	}
	return queuedReset + runningReset, nil
}

// CheckOrInsert checks if a task is already processed or processing.
func (db *DB) CheckOrInsert(filePath string) (bool, error) {
	return db.CheckOrInsertWithForce(filePath, 0, false)
}

// CheckOrInsertWithPayload registers a durable task and stores the complete
// track payload before the HTTP request is acknowledged.
func (db *DB) CheckOrInsertWithPayload(filePath string, trackNumber int, payloadJSON string, force bool) (bool, error) {
	return db.checkOrInsertWithPayload(filePath, trackNumber, payloadJSON, force)
}

// CheckOrInsertWithForce checks if a task should be executed via async writer channel.
// Optimized with Read-First pattern for fast parallel checks without write channel bottleneck.
func (db *DB) CheckOrInsertWithForce(filePath string, trackNumber int, force bool) (bool, error) {
	return db.checkOrInsertWithPayload(filePath, trackNumber, "", force)
}

func (db *DB) checkOrInsertWithPayload(filePath string, trackNumber int, payloadJSON string, force bool) (bool, error) {
	// 1. Fast parallel read (WAL concurrent read)
	if !force {
		var status string
		err := db.conn.QueryRow(`SELECT status FROM task_state WHERE file_path = ? AND track_number = ?`, filePath, trackNumber).Scan(&status)
		if err == nil {
			// Already completed, active, or durably queued -> skip immediately.
			if status == string(StatusCompleted) || status == string(StatusRunning) || status == string(StatusPending) || status == string(StatusQueued) {
				return false, nil
			}
		} else if err != sql.ErrNoRows {
			return false, err
		}
		// If ErrNoRows or a retryable failure, proceed to the serialized writer.
	}

	// 2. Write path (Serialized via writerLoop channel)
	resChan := make(chan dbWriteResult, 1)
	db.opQueue <- dbWriteOp{
		opType:      "check_or_insert",
		filePath:    filePath,
		trackNumber: trackNumber,
		payloadJSON: payloadJSON,
		force:       force,
		resChan:     resChan,
	}
	res := <-resChan
	return res.shouldRun, res.err
}

func (db *DB) execCheckOrInsert(filePath string, trackNumber int, payloadJSON string, force bool) (bool, error) {
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
		if force || status == string(StatusFailed) || status == string(StatusFailedMaybeRetry) {
			_, err = tx.Exec(`
				UPDATE task_state
				SET status = ?, error_message = NULL,
					payload_json = CASE WHEN ? <> '' THEN ? ELSE payload_json END,
					priority_score = CASE WHEN ? <> '' THEN ? ELSE priority_score END,
					updated_at = CURRENT_TIMESTAMP
				WHERE file_path = ? AND track_number = ?
			`, StatusPending, payloadJSON, payloadJSON, payloadJSON, estimatePriorityScore(payloadJSON), filePath, trackNumber)
			if err != nil {
				return false, err
			}
			return true, tx.Commit()
		}
		if status == string(StatusCompleted) || status == string(StatusRunning) || status == string(StatusPending) || status == string(StatusQueued) {
			return false, nil
		}
	}

	_, err = tx.Exec(`INSERT INTO task_state (file_path, track_number, status, payload_json, priority_score) VALUES (?, ?, ?, ?, ?)`, filePath, trackNumber, StatusPending, payloadJSON, estimatePriorityScore(payloadJSON))
	if err != nil {
		return false, err
	}

	return true, tx.Commit()
}

// ClaimPendingTasks atomically moves a bounded batch from durable PENDING to
// in-memory QUEUED ownership. Only the feeder calls this method, but the
// transaction also protects against a second dispatcher instance.
func (db *DB) ClaimPendingTasks(limit int) ([]QueuedTask, error) {
	if limit <= 0 {
		return nil, nil
	}
	resChan := make(chan dbWriteResult, 1)
	db.opQueue <- dbWriteOp{opType: "claim_pending", limit: limit, resChan: resChan}
	res := <-resChan
	return res.tasks, res.err
}

func (db *DB) execClaimPending(limit int) ([]QueuedTask, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("failed to begin pending-task claim: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT file_path, track_number, COALESCE(payload_json, '')
		FROM task_state
		WHERE status = ?
		ORDER BY
			CASE WHEN priority_score > 0 THEN priority_score ELSE 1.0e18 END /
				(1.0 + MAX(0.0, (julianday('now') - julianday(updated_at)) * 86400.0 / 1800.0)) ASC,
			updated_at ASC, file_path ASC, track_number ASC
		LIMIT ?
	`, StatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to select pending tasks: %w", err)
	}

	var tasks []QueuedTask
	for rows.Next() {
		var task QueuedTask
		if err := rows.Scan(&task.FilePath, &task.TrackNumber, &task.PayloadJSON); err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan pending task: %w", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("failed to iterate pending tasks: %w", err)
	}
	rows.Close()

	for _, task := range tasks {
		if _, err := tx.Exec(`
			UPDATE task_state
			SET status = ?, updated_at = CURRENT_TIMESTAMP
			WHERE file_path = ? AND track_number = ? AND status = ?
		`, StatusQueued, task.FilePath, task.TrackNumber, StatusPending); err != nil {
			return nil, fmt.Errorf("failed to claim task %s track %d: %w", task.FilePath, task.TrackNumber, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("failed to commit pending-task claim: %w", err)
	}
	return tasks, nil
}

// RequeueRetryableTasks makes old FAILED_MAYBE_RETRY rows eligible again only
// after a cooldown. This prevents a permanently starved machine from spinning
// the same task in a tight loop while still allowing recovery when resources
// return and the normal queue is empty.
func (db *DB) RequeueRetryableTasks(limit, minAgeSec int) (int64, error) {
	if limit <= 0 {
		return 0, nil
	}
	resChan := make(chan dbWriteResult, 1)
	db.opQueue <- dbWriteOp{
		opType:  "requeue_retryable",
		limit:   limit,
		errMsg:  rescheduleAgeModifier(minAgeSec),
		resChan: resChan,
	}
	res := <-resChan
	if res.err != nil {
		return 0, res.err
	}
	return res.count, nil
}

func (db *DB) execRequeueRetryable(limit int, ageModifier string) (int64, error) {
	res, err := db.conn.Exec(`
		UPDATE task_state
		SET status = ?, error_message = 'Automatic retry released after durable queue drained', updated_at = CURRENT_TIMESTAMP
		WHERE rowid IN (
			SELECT rowid FROM task_state
			WHERE status = ?
			  AND updated_at <= datetime('now', ?)
			ORDER BY updated_at ASC, file_path ASC, track_number ASC
			LIMIT ?
		)
	`, StatusPending, StatusFailedMaybeRetry, ageModifier, limit)
	if err != nil {
		return 0, fmt.Errorf("failed to requeue retryable tasks: %w", err)
	}
	return res.RowsAffected()
}

func rescheduleAgeModifier(minAgeSec int) string {
	return fmt.Sprintf("-%d seconds", maxInt(minAgeSec, 0))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// UpdateStatus enqueues an asynchronous non-blocking status update.
func (db *DB) UpdateStatus(filePath string, trackNumber int, status TaskStatus, errMsg string) error {
	db.opQueue <- dbWriteOp{
		opType:      "update_status",
		filePath:    filePath,
		trackNumber: trackNumber,
		status:      status,
		errMsg:      errMsg,
		resChan:     nil, // Fire-and-forget (Non-blocking!)
	}
	return nil
}

func (db *DB) execUpdateStatus(filePath string, trackNumber int, status TaskStatus, errMsg string) error {
	_, err := db.conn.Exec(`
		UPDATE task_state 
		SET status = ?, error_message = ?, updated_at = CURRENT_TIMESTAMP 
		WHERE file_path = ? AND track_number = ?
	`, status, errMsg, filePath, trackNumber)
	return err
}

// ClaimSingleTask atomically reserves exactly one requested track. Active work
// is never stolen, even with force; process-level exclusion protects ResetStaleTasks.
func (db *DB) ClaimSingleTask(filePath string, trackNumber int, payloadJSON string, force, recoverActive bool) (bool, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return false, fmt.Errorf("begin single-task claim: %w", err)
	}
	defer tx.Rollback()

	var status string
	err = tx.QueryRow(`SELECT status FROM task_state WHERE file_path = ? AND track_number = ?`, filePath, trackNumber).Scan(&status)
	if err == sql.ErrNoRows {
		_, err = tx.Exec(`INSERT INTO task_state (file_path, track_number, status, error_message, payload_json, priority_score, updated_at)
			VALUES (?, ?, ?, NULL, ?, ?, CURRENT_TIMESTAMP)`, filePath, trackNumber, StatusQueued, payloadJSON, estimatePriorityScore(payloadJSON))
		if err != nil {
			return false, fmt.Errorf("insert single-task claim: %w", err)
		}
		return true, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("read single-task state: %w", err)
	}
	if (status == string(StatusQueued) || status == string(StatusRunning)) && !recoverActive {
		return false, fmt.Errorf("task is already active with status %s", status)
	}
	if status == string(StatusCompleted) && !force {
		return false, nil
	}
	res, err := tx.Exec(`UPDATE task_state SET status = ?, error_message = NULL, payload_json = ?, priority_score = ?, updated_at = CURRENT_TIMESTAMP
		WHERE file_path = ? AND track_number = ? AND status = ?`, StatusQueued, payloadJSON, estimatePriorityScore(payloadJSON), filePath, trackNumber, status)
	if err != nil {
		return false, fmt.Errorf("update single-task claim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return false, fmt.Errorf("single-task claim lost race (affected=%d): %w", n, err)
	}
	return true, tx.Commit()
}

func (db *DB) Flush() error {
	resChan := make(chan dbWriteResult, 1)
	db.opQueue <- dbWriteOp{opType: "flush", resChan: resChan}
	return (<-resChan).err
}

func (db *DB) GetTaskState(filePath string, trackNumber int) (TaskState, error) {
	var status string
	var errMsg sql.NullString
	err := db.conn.QueryRow(`SELECT status, error_message FROM task_state WHERE file_path = ? AND track_number = ?`, filePath, trackNumber).Scan(&status, &errMsg)
	if err != nil {
		return TaskState{}, err
	}
	return TaskState{Status: TaskStatus(status), ErrorMessage: errMsg.String}, nil
}

func (db *DB) Close() error {
	if db.opQueue != nil {
		close(db.opQueue)
	}
	return db.conn.Close()
}
