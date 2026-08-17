package dispatcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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

func TestDLQFallbackDirectly(t *testing.T) {
	d := &Dispatcher{}
	tmpDir := t.TempDir()
	origWd, _ := os.Getwd()
	_ = os.Chdir(tmpDir)
	defer func() { _ = os.Chdir(origWd) }()

	payload := IngestPayload{
		TrackHash: "0123456789abcdef0123456789abcdef",
		Task: TaskPayload{
			FlacPath:    filepath.Join(tmpDir, "test.flac"),
			TrackNumber: 1,
			Title:       "Fallback Song",
			Artist:      "Fallback Artist",
		},
		LibrosaJSON:  json.RawMessage(`{"features": {"mix": {"bpm": 120.0}}}`),
		EssentiaJSON: json.RawMessage(`{"predictions": {"mood_happy": 0.8}}`),
		TensorJSON:   json.RawMessage(`{"features": {"mix": {"spectral_flux_mean": 5.0}}}`),
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
}
