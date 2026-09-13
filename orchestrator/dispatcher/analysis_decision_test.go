package dispatcher

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDecideAnalysisTable(t *testing.T) {
	now := time.Now()
	completeFeatures := `{"mix":{"bpm":120},"demucs":{"bass":{"rms":1},"drums":{"rms":1},"vocals":{"rms":1},"other":{"rms":1},"guitar":{"rms":1},"piano":{"rms":1}}}`
	withoutMix := `{"demucs":{"bass":{"rms":1},"drums":{"rms":1},"vocals":{"rms":1},"other":{"rms":1},"guitar":{"rms":1},"piano":{"rms":1}}}`
	withoutStems := `{"mix":{"bpm":120}}`
	validMeta := `{"analysis_schema_version":1}`
	validPredictions := `{"genre":0.9}`

	tests := []struct {
		name     string
		snapshot AnalysisSnapshot
		want     AnalysisDecision
	}{
		{name: "absent row", snapshot: AnalysisSnapshot{}, want: FullAnalysis},
		{name: "missing mix", snapshot: snapshotForTest(&now, validMeta, withoutMix, validPredictions), want: MixOnly},
		{name: "missing stems", snapshot: snapshotForTest(&now, validMeta, withoutStems, validPredictions), want: StemsOnly},
		{name: "complete", snapshot: snapshotForTest(&now, validMeta, completeFeatures, validPredictions), want: Skip},
		{name: "old schema", snapshot: snapshotForTest(&now, `{"analysis_schema_version":0}`, completeFeatures, validPredictions), want: FullAnalysis},
		{name: "malformed JSON", snapshot: snapshotForTest(&now, validMeta, `{`, validPredictions), want: FullAnalysis},
		{name: "missing analyzed_at", snapshot: snapshotForTest(nil, validMeta, completeFeatures, validPredictions), want: FullAnalysis},
		{name: "missing predictions", snapshot: snapshotForTest(&now, validMeta, completeFeatures, `{}`), want: MixOnly},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecideAnalysis(tt.snapshot)
			if got.Decision != tt.want {
				t.Fatalf("decision=%s reason=%s want=%s", got.Decision, got.Reason, tt.want)
			}
		})
	}
}

func TestValidateAnalysisDecisionRejectsUnknown(t *testing.T) {
	if err := validateAnalysisDecision(AnalysisDecision("partial")); err == nil {
		t.Fatal("expected unknown decision rejection")
	}
}

func snapshotForTest(analyzedAt *time.Time, meta, features, predictions string) AnalysisSnapshot {
	return AnalysisSnapshot{
		Exists: true, Meta: json.RawMessage(meta), Features: json.RawMessage(features),
		Predictions: json.RawMessage(predictions), AnalyzedAt: analyzedAt, RowVersion: 42,
	}
}
