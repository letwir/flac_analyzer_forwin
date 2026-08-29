package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"flac_analyzer/orchestrator/state"
)

const unregRegistrationQuery = `
SELECT filepath, track_number
FROM raw.library_flac
WHERE analyzed_at IS NOT NULL`

type unregLookup interface {
	Ping(context.Context) error
	SQLiteTaskState(string, int) (state.TaskState, error)
	PostgreSQLRegistrations(context.Context) (map[unregTrackKey]struct{}, error)
}

type databaseUnregLookup struct {
	sqlite  *state.DB
	pg      *sql.DB
	timeout time.Duration
}

// UnregPreflightResult contains the complete read-only decision for one FLAC.
// Callers must not execute any track until this result has been returned.
type UnregPreflightResult struct {
	Eligible          []TaskPayload
	SQLiteSkipped     int
	PostgreSQLSkipped int
}

type sqliteUnregDecision uint8

type unregTrackKey struct {
	path        string
	trackNumber int
}

const (
	sqliteUnregEligible sqliteUnregDecision = iota
	sqliteUnregSkip
)

func canonicalSingleFilePath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve FLAC path: %w", err)
	}
	return filepath.Clean(absPath), nil
}

func registrationComparisonKey(path string) (string, error) {
	canonical, err := canonicalSingleFilePath(path)
	if err != nil {
		return "", err
	}
	return strings.ToLower(strings.ReplaceAll(canonical, "/", `\`)), nil
}

func newUnregTrackKey(path string, trackNumber int) (unregTrackKey, error) {
	key, err := registrationComparisonKey(path)
	if err != nil {
		return unregTrackKey{}, err
	}
	return unregTrackKey{path: key, trackNumber: trackNumber}, nil
}

func classifySQLiteUnregStatus(status state.TaskStatus) (sqliteUnregDecision, error) {
	switch status {
	case state.StatusFailed, state.StatusFailedMaybeRetry:
		return sqliteUnregEligible, nil
	case state.StatusPending, state.StatusQueued, state.StatusRunning, state.StatusCompleted:
		return sqliteUnregSkip, nil
	default:
		return 0, fmt.Errorf("unknown SQLite task status %q", status)
	}
}

func filterUnregisteredSingleTasks(ctx context.Context, tasks []TaskPayload, lookup unregLookup) (UnregPreflightResult, error) {
	if err := lookup.Ping(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return UnregPreflightResult{}, fmt.Errorf("PostgreSQL registration preflight cancelled: %w", err)
		}
		return UnregPreflightResult{}, errors.New("PostgreSQL registration preflight unavailable")
	}
	postgresRegistrations, err := lookup.PostgreSQLRegistrations(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return UnregPreflightResult{}, fmt.Errorf("PostgreSQL registration catalog lookup cancelled: %w", err)
		}
		return UnregPreflightResult{}, errors.New("PostgreSQL registration catalog lookup failed")
	}

	result := UnregPreflightResult{Eligible: make([]TaskPayload, 0, len(tasks))}
	for _, task := range tasks {
		if err := ctx.Err(); err != nil {
			return UnregPreflightResult{}, fmt.Errorf("unregistered preflight cancelled before track %d: %w", task.TrackNumber, err)
		}
		canonical, err := canonicalSingleFilePath(task.FlacPath)
		if err != nil {
			return UnregPreflightResult{}, fmt.Errorf("canonicalize track %d path: %w", task.TrackNumber, err)
		}
		task.FlacPath = canonical

		taskState, stateErr := lookup.SQLiteTaskState(task.FlacPath, task.TrackNumber)
		switch {
		case stateErr == nil:
			decision, err := classifySQLiteUnregStatus(taskState.Status)
			if err != nil {
				return UnregPreflightResult{}, fmt.Errorf("classify SQLite track %d: %w", task.TrackNumber, err)
			}
			if decision == sqliteUnregSkip {
				result.SQLiteSkipped++
				continue
			}
		case errors.Is(stateErr, sql.ErrNoRows):
			// An absent SQLite row remains eligible for the PostgreSQL check.
		default:
			return UnregPreflightResult{}, fmt.Errorf("read SQLite registration for track %d: %w", task.TrackNumber, stateErr)
		}

		trackKey, err := newUnregTrackKey(task.FlacPath, task.TrackNumber)
		if err != nil {
			return UnregPreflightResult{}, fmt.Errorf("normalize PostgreSQL registration key for track %d: %w", task.TrackNumber, err)
		}
		if _, registered := postgresRegistrations[trackKey]; registered {
			result.PostgreSQLSkipped++
			continue
		}
		result.Eligible = append(result.Eligible, task)
	}
	return result, nil
}

func (l databaseUnregLookup) Ping(ctx context.Context) error {
	if l.pg == nil {
		return errors.New("PostgreSQL connection is unavailable")
	}
	pingCtx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.pg.PingContext(pingCtx)
}

func (l databaseUnregLookup) SQLiteTaskState(path string, trackNumber int) (state.TaskState, error) {
	return l.sqlite.GetTaskState(path, trackNumber)
}

func (l databaseUnregLookup) PostgreSQLRegistrations(ctx context.Context) (map[unregTrackKey]struct{}, error) {
	queryCtx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	rows, err := l.pg.QueryContext(queryCtx, unregRegistrationQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	registrations := make(map[unregTrackKey]struct{})
	for rows.Next() {
		var path string
		var trackNumber int
		if err := rows.Scan(&path, &trackNumber); err != nil {
			return nil, err
		}
		key, err := newUnregTrackKey(path, trackNumber)
		if err != nil {
			return nil, err
		}
		registrations[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return registrations, nil
}

func unregDBTimeout(cfg Config) time.Duration {
	seconds := cfg.DBTimeoutSec
	if seconds <= 0 {
		seconds = 20
	}
	return time.Duration(seconds) * time.Second
}

// FilterUnregisteredSingleTasks performs a complete dual-DB preflight using
// the dispatcher's existing connections. It never claims or executes tasks.
func (d *Dispatcher) FilterUnregisteredSingleTasks(ctx context.Context, tasks []TaskPayload) (UnregPreflightResult, error) {
	return filterUnregisteredSingleTasks(ctx, tasks, databaseUnregLookup{
		sqlite:  d.db,
		pg:      d.pgDB,
		timeout: unregDBTimeout(d.config),
	})
}

// CheckUnregisteredSingleFile expands CUE metadata and performs a read-only
// preflight without constructing analyzer pools. It is used by -check-only.
func CheckUnregisteredSingleFile(ctx context.Context, cfg Config, sqliteDB *state.DB, payload TaskPayload) (UnregPreflightResult, error) {
	if strings.TrimSpace(cfg.DatabaseURL) == "" {
		return UnregPreflightResult{}, errors.New("PostgreSQL registration preflight is not configured")
	}
	pg, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		return UnregPreflightResult{}, errors.New("PostgreSQL registration preflight connection could not be initialized")
	}
	defer pg.Close()

	inspector := &Dispatcher{
		config:       cfg,
		executionCtx: ctx,
		logLevel:     cfg.LogLevel,
		eventLog:     cfg.EventLog,
	}
	tasks, err := inspector.ExpandSingleFile(payload)
	if err != nil {
		return UnregPreflightResult{}, err
	}
	return filterUnregisteredSingleTasks(ctx, tasks, databaseUnregLookup{
		sqlite:  sqliteDB,
		pg:      pg,
		timeout: unregDBTimeout(cfg),
	})
}
