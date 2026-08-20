// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// Mor: TrackAudioHash -> (ExistsInDB, Error)
package dispatcher

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// CheckHashExistsInPostgres queries PostgreSQL directly in Go to check if audio_hash already exists.
// SideEffectFn: CheckHashExistsInPostgres (IO Monad)
func (d *Dispatcher) CheckHashExistsInPostgres(trackHash string) (bool, error) {
	if d.pgDB == nil || trackHash == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var exists int
	err := d.pgDB.QueryRowContext(ctx, "SELECT 1 FROM raw.library_flac WHERE audio_hash = $1 LIMIT 1", trackHash).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("postgres audio_hash lookup failed: %w", err)
	}
	return exists == 1, nil
}
