package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// SelectEmptyJSONRepair selects exactly one existing track. Healthy records
// produce an empty plan; missing/moved tracks fail rather than expanding scope.
func (d *Dispatcher) SelectEmptyJSONRepair(ctx context.Context, tasks []TaskPayload, recordID int64) ([]TaskPayload, error) {
	if d.pgDB == nil || recordID <= 0 {
		return nil, errors.New("repair requires PostgreSQL and a positive record ID")
	}
	var path, hash string
	var track int
	var empty bool
	if err := d.pgDB.QueryRowContext(ctx,
		`SELECT filepath, track_number, audio_hash, features = '{}' FROM raw.library_flac WHERE id = $1`, recordID,
	).Scan(&path, &track, &hash, &empty); err != nil {
		return nil, fmt.Errorf("read repair record %d: %w", recordID, err)
	}
	if !empty {
		return nil, nil
	}
	if hash == "" {
		return nil, errors.New("repair record has no audio hash")
	}
	var selected []TaskPayload
	for _, task := range tasks {
		// Exact path is also required by the guarded UPDATE below.
		if task.FlacPath == path && task.TrackNumber == track {
			task.Force = true
			task.RepairRecordID, task.RepairAudioHash = recordID, hash
			selected = append(selected, task)
		}
	}
	if len(selected) != 1 {
		return nil, fmt.Errorf("record %d does not match exactly one CUE track", recordID)
	}
	return selected, nil
}

const repairEmptyJSONQuery = `UPDATE raw.library_flac
SET meta = CASE WHEN meta = '{}' THEN $1 ELSE meta END,
    features = $2,
    predictions = CASE WHEN predictions = '{}' THEN $3 ELSE predictions END,
    analyzed_at = CURRENT_TIMESTAMP
WHERE id = $4 AND filepath = $5 AND track_number = $6
  AND audio_hash = $7 AND features = '{}'`

// Repairs never use an UPSERT or the generic DLQ replay: either could overwrite
// a now-healthy record or create a new row after the selected audio changed.
func (d *Dispatcher) updateEmptyJSONRecord(ctx context.Context, payload IngestPayload, meta, features, predictions json.RawMessage) IngestResult {
	task := payload.Task
	if d.pgDB == nil || task.RepairAudioHash == "" || payload.TrackHash != task.RepairAudioHash {
		return IngestResult{ErrorMessage: "repair requires the original audio hash and PostgreSQL connection"}
	}
	result, err := d.pgDB.ExecContext(ctx, repairEmptyJSONQuery,
		string(meta), string(features), string(predictions), task.RepairRecordID,
		task.FlacPath, task.TrackNumber, task.RepairAudioHash)
	if err != nil {
		return IngestResult{ErrorMessage: fmt.Sprintf("guarded repair failed: %v", err)}
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return IngestResult{ErrorMessage: "repair guard rejected update: record changed, disappeared, or is no longer empty"}
	}
	return IngestResult{Success: true}
}
