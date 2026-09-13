package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lib/pq"
)

const analysisPreflightQuery = `
WITH requested(filepath_key, track_number) AS (
	SELECT * FROM unnest($1::text[], $2::integer[])
)
SELECT library.id, library.filepath, COALESCE(library.track_number, 1),
	library.audio_hash, library.meta, library.features, library.predictions,
	library.analyzed_at, library.xmin::text::bigint
FROM raw.library_flac AS library
JOIN requested
	ON lower(replace(library.filepath, '/', E'\\')) = requested.filepath_key
	AND COALESCE(library.track_number, 1) = requested.track_number`

type analysisSnapshotRow struct {
	key      unregTrackKey
	snapshot AnalysisSnapshot
}

type analysisSnapshotLookup interface {
	Lookup(context.Context, []unregTrackKey) ([]analysisSnapshotRow, error)
}

type databaseAnalysisSnapshotLookup struct {
	pg      *sql.DB
	timeout time.Duration
}

func (l databaseAnalysisSnapshotLookup) Lookup(ctx context.Context, requested []unregTrackKey) ([]analysisSnapshotRow, error) {
	if l.pg == nil {
		return nil, errors.New("PostgreSQL analysis preflight is unavailable")
	}
	if len(requested) == 0 {
		return nil, nil
	}
	paths := make([]string, len(requested))
	tracks := make([]int, len(requested))
	for i, key := range requested {
		paths[i], tracks[i] = key.path, key.trackNumber
	}
	queryCtx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	rows, err := l.pg.QueryContext(queryCtx, analysisPreflightQuery, pq.Array(paths), pq.Array(tracks))
	if err != nil {
		return nil, fmt.Errorf("query PostgreSQL analysis snapshots: %w", err)
	}
	defer rows.Close()

	result := make([]analysisSnapshotRow, 0, len(requested))
	for rows.Next() {
		var row analysisSnapshotRow
		var path, meta, features, predictions string
		var track int
		if err := rows.Scan(&row.snapshot.RowID, &path, &track, &row.snapshot.AudioHash, &meta, &features, &predictions, &row.snapshot.AnalyzedAt, &row.snapshot.RowVersion); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL analysis snapshot: %w", err)
		}
		row.key, err = newUnregTrackKey(path, track)
		if err != nil {
			return nil, fmt.Errorf("normalize PostgreSQL analysis snapshot: %w", err)
		}
		row.snapshot.Exists = true
		row.snapshot.Meta = json.RawMessage(meta)
		row.snapshot.Features = json.RawMessage(features)
		row.snapshot.Predictions = json.RawMessage(predictions)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate PostgreSQL analysis snapshots: %w", err)
	}
	return result, nil
}

func prepareAnalysisTasks(ctx context.Context, tasks []TaskPayload, lookup analysisSnapshotLookup) ([]TaskPayload, error) {
	keys := make([]unregTrackKey, len(tasks))
	normalized := make([]TaskPayload, len(tasks))
	for i, task := range tasks {
		key, err := newUnregTrackKey(task.FlacPath, task.TrackNumber)
		if err != nil {
			return nil, fmt.Errorf("normalize analysis key for track %d: %w", task.TrackNumber, err)
		}
		task.TrackNumber = key.trackNumber
		normalized[i], keys[i] = task, key
	}
	rows, err := lookup.Lookup(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("analysis preflight failed: %w", err)
	}
	byKey := make(map[unregTrackKey]AnalysisSnapshot, len(rows))
	for _, row := range rows {
		if _, duplicate := byKey[row.key]; duplicate {
			return nil, fmt.Errorf("analysis preflight returned duplicate row for track %d", row.key.trackNumber)
		}
		byKey[row.key] = row.snapshot
	}
	for i, key := range keys {
		snapshot := byKey[key]
		decision := DecideAnalysis(snapshot)
		if normalized[i].Force {
			decision = AnalysisDecisionResult{Decision: FullAnalysis, Reason: "forced", RowVersion: snapshot.RowVersion}
		}
		normalized[i].AnalysisDecision = decision.Decision
		normalized[i].AnalysisRowID = snapshot.RowID
		normalized[i].AnalysisRowVersion = snapshot.RowVersion
		normalized[i].AnalysisAudioHash = snapshot.AudioHash
	}
	return normalized, nil
}

func (d *Dispatcher) prepareAnalysisTasks(ctx context.Context, tasks []TaskPayload) ([]TaskPayload, error) {
	if d.prepareAnalysisFn != nil {
		return d.prepareAnalysisFn(ctx, tasks)
	}
	return prepareAnalysisTasks(ctx, tasks, databaseAnalysisSnapshotLookup{pg: d.pgDB, timeout: unregDBTimeout(d.GetConfig())})
}
