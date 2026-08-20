// Package dispatcher provides the task dispatching and IO monad operations for flac_analyzer.
// Mor(TaskPayload * FeaturePayload -> IngestResult)
// Functor(f o g) | Semantics(Category: IO Monad Ingestion Effect)
package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"flac_analyzer/orchestrator/metrics"
	"flac_analyzer/orchestrator/state"

	_ "modernc.org/sqlite"
)

// IngestPayload は各ワーカーから収集された特徴量・推論結果のインメモリ統合データですわ！
type IngestPayload struct {
	TrackHash       string          `json:"track_hash"`
	Task            TaskPayload     `json:"task"`
	LibrosaJSON     json.RawMessage `json:"librosa_json"`
	EssentiaJSON    json.RawMessage `json:"essentia_json"`
	TensorJSON      json.RawMessage `json:"tensor_json"`
}

// IngestResult は PostgreSQL への UPSERT 結果を保持するモナド構造体ですの。
type IngestResult struct {
	Success      bool          `json:"success"`
	SavedToDLQ   bool          `json:"saved_to_dlq"`
	DBDuration   time.Duration `json:"db_duration"`
	ErrorMessage string        `json:"error_message,omitempty"`
}

// MergeFeaturesAndPredictions は Librosa, Tensor, Essentia の各生JSONを PostgreSQL の features / predictions 構造へ統合する純粋射ですわ！
func MergeFeaturesAndPredictions(librosaRaw, essentiaRaw, tensorRaw json.RawMessage) (json.RawMessage, json.RawMessage, json.RawMessage, error) {
	// 1. Librosa features & meta のパース
	var libData struct {
		Meta     map[string]interface{} `json:"meta"`
		Features map[string]interface{} `json:"features"`
	}
	if len(librosaRaw) > 0 {
		if err := json.Unmarshal(librosaRaw, &libData); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to unmarshal librosa JSON: %w", err)
		}
	}
	if libData.Features == nil {
		libData.Features = make(map[string]interface{})
	}
	if libData.Meta == nil {
		libData.Meta = make(map[string]interface{})
	}

	// 2. Tensor features のマージ (mix および demucs サブステムへ注入)
	if len(tensorRaw) > 0 {
		mergeTensorFeaturesPure(libData.Features, tensorRaw)
	}

	// 3. Essentia predictions のパース
	var essData struct {
		Predictions map[string]interface{} `json:"predictions"`
	}
	if len(essentiaRaw) > 0 {
		_ = json.Unmarshal(essentiaRaw, &essData)
	}
	if essData.Predictions == nil {
		essData.Predictions = make(map[string]interface{})
	}

	mergedFeatures, err := json.Marshal(libData.Features)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal merged features: %w", err)
	}

	mergedPredictions, err := json.Marshal(essData.Predictions)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal merged predictions: %w", err)
	}

	metaJSON, err := json.Marshal(libData.Meta)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to marshal meta: %w", err)
	}

	return metaJSON, mergedFeatures, mergedPredictions, nil
}

// mergeTensorFeaturesPure は Tensor 特徴量 JSON を Librosa features マップへマージする純粋補助射ですわ！
func mergeTensorFeaturesPure(libFeatures map[string]interface{}, tensorRaw json.RawMessage) {
	var tensorData struct {
		Features map[string]interface{} `json:"features"`
	}
	if err := json.Unmarshal(tensorRaw, &tensorData); err != nil || tensorData.Features == nil {
		return
	}

	for stemName, stemFeats := range tensorData.Features {
		featsMap, isMap := stemFeats.(map[string]interface{})
		if !isMap {
			continue
		}

		if stemName == "mix" {
			mixMap, ok := libFeatures["mix"].(map[string]interface{})
			if !ok {
				mixMap = make(map[string]interface{})
				libFeatures["mix"] = mixMap
			}
			for k, v := range featsMap {
				mixMap[k] = v
			}
			continue
		}

		if stemName == "demucs" {
			demucsMap, ok := libFeatures["demucs"].(map[string]interface{})
			if !ok {
				demucsMap = make(map[string]interface{})
				libFeatures["demucs"] = demucsMap
			}
			for subStem, subFeats := range featsMap {
				subVals, isSubMap := subFeats.(map[string]interface{})
				if !isSubMap {
					continue
				}
				targetSub, subOk := demucsMap[subStem].(map[string]interface{})
				if !subOk {
					targetSub = make(map[string]interface{})
					demucsMap[subStem] = targetSub
				}
				for k, v := range subVals {
					targetSub[k] = v
				}
			}
		}
	}
}

// ingestWorker はメインの解析ワーカーから非同期にペイロードを受け取り、
// タイムアウト付きで PostgreSQL または SQLite DLQ へ永続化する独立バックグラウンド射ですわ！
func (d *Dispatcher) ingestWorker() {
	defer d.ingestWg.Done()

	for payload := range d.ingestQueue {
		d.processIngestPayloadComplex(payload)
	}
}

// processIngestPayloadComplex は1件の IngestPayload に対して Panic Recovery を施し、
// タイムアウト付きの DB UPSERT / DLQ 退避・状態更新・メトリクス計測を完遂する堅牢な IO 射ですわ！
func (d *Dispatcher) processIngestPayloadComplex(payload IngestPayload) {
	// Advisory 3: 予期せぬ panic を安全にキャッチして IngestWorker の脱落を完全防止
	defer func() {
		if r := recover(); r != nil {
			d.LogError("[IngestWorker] Panic recovered during ingestion for %s (Track %d): %v", payload.Task.FlacPath, payload.Task.TrackNumber, r)
			_ = d.saveToSQLiteDLQ(payload.TrackHash, payload.Task, filepath.Base(payload.Task.FlacPath), payload.LibrosaJSON, payload.TensorJSON, payload.EssentiaJSON)
			d.db.UpdateStatus(payload.Task.FlacPath, payload.Task.TrackNumber, state.StatusFailed, fmt.Sprintf("Panic in IngestWorker: %v", r))
			metrics.AnalyzerTasksTotal.WithLabelValues("error").Inc()
		}
	}()

	currentCfg := d.GetConfig()
	timeoutSec := currentCfg.DBTimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 20
	}
	dbTimeout := time.Duration(timeoutSec) * time.Second

	// Advisory 4: context.WithTimeout に対する RAII defer cancel() の徹底
	ctx, cancel := context.WithTimeout(d.ingestCtx, dbTimeout)
	defer cancel()

	ingestStart := time.Now()
	ingestRes := d.UpsertTrackDirectly(ctx, payload)

	if d.statsTracker != nil {
		d.statsTracker.RecordStageDuration("db_ingest", time.Since(ingestStart))
	}

	task := payload.Task
	if !ingestRes.Success {
		d.LogError("[IngestWorker] DB and DLQ both failed for %s (Track %d): %s", task.FlacPath, task.TrackNumber, ingestRes.ErrorMessage)
		d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusFailed, fmt.Sprintf("Ingestion failed: %s", ingestRes.ErrorMessage))
		metrics.AnalyzerTasksTotal.WithLabelValues("error").Inc()
		return
	}

	if ingestRes.SavedToDLQ {
		d.LogWarn("[IngestWorker] DLQ fallback triggered for %s (Track %d). Preserved in send_failed.db.", task.FlacPath, task.TrackNumber)
		d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusCompleted, "Saved to DLQ (send_failed.db)")
		metrics.AnalyzerTasksTotal.WithLabelValues("success").Inc()
		return
	}

	d.LogInfo("[IngestWorker] Successfully ingested into PostgreSQL: %s (Track %d)", task.FlacPath, task.TrackNumber)
	d.db.UpdateStatus(task.FlacPath, task.TrackNumber, state.StatusCompleted, "")
	metrics.AnalyzerTasksTotal.WithLabelValues("success").Inc()
}

// UpsertTrackDirectly は Go オーケストレーターから直接 PostgreSQL (raw.library_flac) へ UPSERT を敢行し、
// 接続障害・タイムアウト時は SQLite DLQ (send_failed.db) へ完全フォールバックする IO エフェクト射ですわ！
func (d *Dispatcher) UpsertTrackDirectly(ctx context.Context, payload IngestPayload) IngestResult {
	metaJSON, featuresJSON, predictionsJSON, err := MergeFeaturesAndPredictions(
		payload.LibrosaJSON,
		payload.EssentiaJSON,
		payload.TensorJSON,
	)
	if err != nil {
		return IngestResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("Feature merge error: %v", err),
		}
	}

	filename := filepath.Base(payload.Task.FlacPath)
	task := payload.Task

	// 1. PostgreSQL への直接 UPSERT を試行 (タイムアウト付き context で厳格保護)
	if d.pgDB != nil {
		tQueryStart := time.Now()
		query := `
			INSERT INTO raw.library_flac (
				audio_hash, filepath, filename, track_number, album_artist, album, artist, title, meta, features, predictions, analyzed_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, CURRENT_TIMESTAMP
			)
			ON CONFLICT (audio_hash) DO UPDATE SET
				filepath = EXCLUDED.filepath,
				filename = EXCLUDED.filename,
				track_number = EXCLUDED.track_number,
				album_artist = EXCLUDED.album_artist,
				album = EXCLUDED.album,
				artist = EXCLUDED.artist,
				title = EXCLUDED.title,
				meta = EXCLUDED.meta,
				features = EXCLUDED.features,
				predictions = EXCLUDED.predictions,
				analyzed_at = EXCLUDED.analyzed_at;
		`
		_, execErr := d.pgDB.ExecContext(
			ctx,
			query,
			payload.TrackHash,
			task.FlacPath,
			filename,
			task.TrackNumber,
			task.AlbumArtist,
			task.Album,
			task.Artist,
			task.Title,
			string(metaJSON),
			string(featuresJSON),
			string(predictionsJSON),
		)

		if execErr == nil {
			dur := time.Since(tQueryStart)
			d.LogInfo("[DirectIngest] PostgreSQL direct UPSERT succeeded (Hash: %s, Time: %v)", payload.TrackHash, dur)
			return IngestResult{
				Success:    true,
				DBDuration: dur,
			}
		}

		d.LogWarn("[DirectIngest] PostgreSQL UPSERT error/timeout (%v): %v. Falling back to local DLQ (send_failed.db)...", ctx.Err(), execErr)
	}

	// 2. PostgreSQL 未接続または書き込み失敗時のローカル SQLite DLQ フォールバック (Safety Guard)
	dlqErr := d.saveToSQLiteDLQ(payload.TrackHash, task, filename, metaJSON, featuresJSON, predictionsJSON)
	if dlqErr != nil {
		d.LogError("[DirectIngest] Critical: DLQ fallback also failed for %s: %v", payload.TrackHash, dlqErr)
		return IngestResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("PostgreSQL and DLQ both failed: %v", dlqErr),
		}
	}

	d.LogWarn("[DirectIngest] Safely preserved payload in DLQ (send_failed.db) for Hash: %s", payload.TrackHash)
	return IngestResult{
		Success:    true,
		SavedToDLQ: true,
	}
}

// saveToSQLiteDLQ は PostgreSQL 接続不能時にローカル SQLite へ解析結果を退避する不変保護射ですわ。
func (d *Dispatcher) saveToSQLiteDLQ(trackHash string, task TaskPayload, filename string, meta, features, predictions []byte) error {
	parentDir := findProjectRoot()
	dlqPath := filepath.Join(parentDir, "send_failed.db")

	dlqDB, err := sql.Open("sqlite", dlqPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return fmt.Errorf("failed to open DLQ sqlite: %w", err)
	}
	defer dlqDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	schema := `
		CREATE TABLE IF NOT EXISTS failed_payloads (
			audio_hash TEXT PRIMARY KEY,
			filepath TEXT,
			filename TEXT,
			track_number INTEGER,
			album_artist TEXT,
			album TEXT,
			artist TEXT,
			title TEXT,
			meta JSON,
			features JSON,
			predictions JSON,
			failed_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
	`
	if _, err := dlqDB.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("failed to create DLQ schema: %w", err)
	}

	insertSQL := `
		INSERT OR REPLACE INTO failed_payloads (
			audio_hash, filepath, filename, track_number, album_artist, album, artist, title, meta, features, predictions
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	_, err = dlqDB.ExecContext(
		ctx,
		insertSQL,
		trackHash,
		task.FlacPath,
		filename,
		task.TrackNumber,
		task.AlbumArtist,
		task.Album,
		task.Artist,
		task.Title,
		string(meta),
		string(features),
		string(predictions),
	)
	if err != nil {
		return fmt.Errorf("failed to insert payload into DLQ: %w", err)
	}

	return nil
}
