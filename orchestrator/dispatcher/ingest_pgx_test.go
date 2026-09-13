package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"flac_analyzer/orchestrator/state"
)

func TestMergeFeaturesAndPredictions(t *testing.T) {
	libRaw := json.RawMessage(`{
		"meta": {"artist": "TestArtist", "title": "TestTitle"},
		"features": {
			"mix": {"bpm": 128.0, "centroid_mean": 2500.0},
			"demucs": {
				"bass": {"rms_mean": 0.4}
			}
		}
	}`)
	essRaw := json.RawMessage(`{
		"predictions": {
			"genre_electronic": 0.95,
			"danceability": 0.88
		}
	}`)
	tensorRaw := json.RawMessage(`{
		"features": {
			"mix": {"spectral_flux_mean": 12.3, "psd_peak_freq": 440.0},
			"demucs": {
				"bass": {"subbass_env_mean": 0.75}
			}
		}
	}`)

	metaJSON, featuresJSON, predictionsJSON, err := MergeFeaturesAndPredictions(libRaw, essRaw, tensorRaw)
	if err != nil {
		t.Fatalf("MergeFeaturesAndPredictions failed: %v", err)
	}

	var feats map[string]interface{}
	if err := json.Unmarshal(featuresJSON, &feats); err != nil {
		t.Fatalf("Failed to unmarshal merged features: %v", err)
	}

	mixMap, ok := feats["mix"].(map[string]interface{})
	if !ok {
		t.Fatalf("mix map missing in features")
	}
	if mixMap["bpm"] != 128.0 {
		t.Errorf("expected bpm 128.0, got %v", mixMap["bpm"])
	}
	if mixMap["spectral_flux_mean"] != 12.3 {
		t.Errorf("expected spectral_flux_mean 12.3, got %v", mixMap["spectral_flux_mean"])
	}

	demucsMap, ok := feats["demucs"].(map[string]interface{})
	if !ok {
		t.Fatalf("demucs map missing in features")
	}
	bassMap, ok := demucsMap["bass"].(map[string]interface{})
	if !ok {
		t.Fatalf("bass map missing in demucs features")
	}
	if bassMap["rms_mean"] != 0.4 {
		t.Errorf("expected rms_mean 0.4, got %v", bassMap["rms_mean"])
	}
	if bassMap["subbass_env_mean"] != 0.75 {
		t.Errorf("expected subbass_env_mean 0.75, got %v", bassMap["subbass_env_mean"])
	}

	var preds map[string]interface{}
	if err := json.Unmarshal(predictionsJSON, &preds); err != nil {
		t.Fatalf("Failed to unmarshal merged predictions: %v", err)
	}
	if preds["genre_electronic"] != 0.95 {
		t.Errorf("expected genre_electronic 0.95, got %v", preds["genre_electronic"])
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		t.Fatalf("Failed to unmarshal meta: %v", err)
	}
	if meta["artist"] != "TestArtist" {
		t.Errorf("expected artist TestArtist, got %v", meta["artist"])
	}
}

func TestPrepareIngestDaemonResponse(t *testing.T) {
	// Match worker_daemon.py's actual response, including nested scalars/sequences.
	var response DaemonResponse
	if err := json.Unmarshal([]byte(`{"status":"success","librosa":{"mix":{"scalars":{"bpm":128},"sequences":{"chords":["C","G"]}},"demucs":{"vocals":{"scalars":{"rms":0.4}}}},"tensor":{"mix":{"hnr":12},"demucs":{"vocals":{"hnr":9}}},"essentia":{"ESSENTIA_MOOD_HAPPY":0.8}}`), &response); err != nil {
		t.Fatal(err)
	}
	encode := func(value any) json.RawMessage {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	payload := IngestPayload{
		Task:        TaskPayload{Title: "Remember", Artist: "Artist", Album: "Album", AlbumArtist: "Various", TrackNumber: 1},
		LibrosaJSON: encode(response.Librosa), TensorJSON: encode(response.Tensor), EssentiaJSON: encode(response.Essentia),
	}
	meta, features, predictions, err := prepareIngestJSON(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(features, &got); err != nil {
		t.Fatal(err)
	}
	mix := got["mix"].(map[string]any)
	if mix["scalars"].(map[string]any)["bpm"] != float64(128) || mix["hnr"] != float64(12) {
		t.Fatalf("lost mix data: %s", features)
	}
	if len(mix["sequences"].(map[string]any)["chords"].([]any)) != 2 {
		t.Fatalf("lost sequences: %s", features)
	}
	vocals := got["demucs"].(map[string]any)["vocals"].(map[string]any)
	if vocals["scalars"].(map[string]any)["rms"] != 0.4 || vocals["hnr"] != float64(9) {
		t.Fatalf("lost stem data: %s", features)
	}
	if string(predictions) != `{"ESSENTIA_MOOD_HAPPY":0.8}` {
		t.Fatalf("lost predictions: %s", predictions)
	}
	if err := json.Unmarshal(meta, &got); err != nil {
		t.Fatal(err)
	}
	if got["title"] != "Remember" || got["track_number"] != float64(1) {
		t.Fatalf("missing metadata: %s", meta)
	}
}

func TestPrepareIngestRejectsInvalidPayload(t *testing.T) {
	for _, tc := range []struct{ name, lib, tensor, ess string }{
		{"empty", `{}`, `{}`, `{}`},
		{"empty stems", `{"mix":{},"demucs":{"vocals":{}}}`, `{}`, `{}`},
		{"unknown shape", `{"unexpected":123}`, `{}`, `{}`},
		{"bad tensor", `{"mix":{"bpm":128}}`, `{`, `{}`},
		{"bad predictions", `{"mix":{"bpm":128}}`, `{}`, `{`},
		{"invalid wrapped tensor", `{"mix":{"bpm":128}}`, `{"features":[]}`, `{}`},
		{"invalid stem", `{"mix":{"bpm":128}}`, `{"demucs":{"vocals":2}}`, `{}`},
		{"null", `null`, `{}`, `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := IngestPayload{LibrosaJSON: json.RawMessage(tc.lib), TensorJSON: json.RawMessage(tc.tensor), EssentiaJSON: json.RawMessage(tc.ess)}
			if _, _, _, err := prepareIngestJSON(payload); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestValidateAnalysisWrite(t *testing.T) {
	if err := validateAnalysisWrite(TaskPayload{}, "hash"); err == nil {
		t.Fatal("missing preflight decision must be rejected")
	}
	task := TaskPayload{AnalysisDecision: StemsOnly, AnalysisRowID: 7, AnalysisAudioHash: "old"}
	if err := validateAnalysisWrite(task, "new"); err == nil {
		t.Fatal("changed source hash must be rejected")
	}
	if err := validateAnalysisWrite(task, "old"); err != nil {
		t.Fatalf("matching snapshot rejected: %v", err)
	}
}

func TestDLQFallbackDirectly(t *testing.T) {
	d := &Dispatcher{}
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	payload := IngestPayload{
		TrackHash: "0123456789abcdef0123456789abcdef",
		Task: TaskPayload{
			FlacPath:         filepath.Join(tmpDir, "test.flac"),
			TrackNumber:      1,
			AnalysisDecision: FullAnalysis,
			Title:            "Fallback Song",
			Artist:           "Fallback Artist",
		},
		LibrosaJSON:  json.RawMessage(`{"mix": {"bpm": 120.0}}`),
		EssentiaJSON: json.RawMessage(`{"mood_happy": 0.8}`),
		TensorJSON:   json.RawMessage(`{"mix": {"spectral_flux_mean": 5.0}}`),
	}

	res := d.UpsertTrackDirectly(context.Background(), payload)
	if !res.Success {
		t.Fatalf("Expected DLQ fallback success, got error: %s", res.ErrorMessage)
	}
	if !res.SavedToDLQ {
		t.Errorf("Expected SavedToDLQ to be true")
	}

	dlqPath := filepath.Join(tmpDir, "send_failed.db")
	if _, err := os.Stat(dlqPath); os.IsNotExist(err) {
		t.Fatalf("Expected send_failed.db to be created, but not found")
	}
	stored, err := sql.Open("sqlite", dlqPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Close()
	var meta, features, predictions string
	if err := stored.QueryRowContext(t.Context(), "SELECT meta, features, predictions FROM failed_payloads WHERE audio_hash = ?", payload.TrackHash).Scan(&meta, &features, &predictions); err != nil {
		t.Fatal(err)
	}
	for label, data := range map[string]string{"meta": meta, "features": features, "predictions": predictions} {
		var values map[string]any
		if err := json.Unmarshal([]byte(data), &values); err != nil || len(values) == 0 {
			t.Fatalf("empty/invalid stored %s: %s (%v)", label, data, err)
		}
	}
	if features != `{"mix":{"bpm":120,"spectral_flux_mean":5}}` {
		t.Fatalf("lost stored features: %s", features)
	}
	if predictions != `{"mood_happy":0.8}` {
		t.Fatalf("lost stored predictions: %s", predictions)
	}
}

func TestIngestWorker_DecoupledPipeline(t *testing.T) {
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	dbPath := filepath.Join(tmpDir, "test_task.db")
	stateDB, err := state.InitDB(dbPath)
	if err != nil {
		t.Fatalf("Failed to init state DB: %v", err)
	}
	defer stateDB.Close()

	testFlac := filepath.Join(tmpDir, "test_song.flac")
	_, _ = stateDB.CheckOrInsertWithForce(testFlac, 1, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := &Dispatcher{
		db:           stateDB,
		ingestQueue:  make(chan IngestPayload, 100),
		ingestCtx:    ctx,
		cancelIngest: cancel,
		config: Config{
			DBTimeoutSec: 2,
		},
	}

	d.ingestWg.Add(1)
	go d.ingestWorker()

	payload := IngestPayload{
		TrackHash: "aabbccddeeff00112233445566778899",
		Task: TaskPayload{
			FlacPath:         testFlac,
			TrackNumber:      1,
			AnalysisDecision: FullAnalysis,
			Title:            "Async Song",
			Artist:           "Async Artist",
		},
		LibrosaJSON:  json.RawMessage(`{"features": {"mix": {"bpm": 130.0}}}`),
		EssentiaJSON: json.RawMessage(`{"predictions": {"mood_happy": 0.9}}`),
		TensorJSON:   json.RawMessage(`{"features": {"mix": {"spectral_flux_mean": 6.0}}}`),
	}

	// エンキュー
	d.ingestQueue <- payload

	// シャットダウン（キューを閉じてワーカー完了を待機）
	close(d.ingestQueue)
	d.ingestWg.Wait()

	// 状態が COMPLETED (Saved to DLQ) に更新されたため、再実行判定で false になることを検証
	shouldRun, err := stateDB.CheckOrInsertWithForce(testFlac, 1, false)
	if err != nil {
		t.Fatalf("Failed to check task status: %v", err)
	}
	if shouldRun {
		t.Errorf("Expected shouldRun to be false after completion, got true")
	}
}
