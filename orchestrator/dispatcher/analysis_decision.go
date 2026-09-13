package dispatcher

import (
	"encoding/json"
	"fmt"
	"time"
)

// AnalysisSchemaVersion is persisted in meta and invalidates incomplete legacy rows.
const AnalysisSchemaVersion = 1

var requiredAnalysisStems = [...]string{"bass", "drums", "vocals", "other", "guitar", "piano"}

type AnalysisDecision string

const (
	FullAnalysis AnalysisDecision = "full"
	MixOnly      AnalysisDecision = "mix_only"
	StemsOnly    AnalysisDecision = "stems_only"
	Skip         AnalysisDecision = "skip"
)

// AnalysisSnapshot is an immutable PostgreSQL preflight result. RowVersion is
// the row's xmin token and must be revalidated before replacing stored output.
type AnalysisSnapshot struct {
	Exists      bool
	Meta        json.RawMessage
	Features    json.RawMessage
	Predictions json.RawMessage
	AnalyzedAt  *time.Time
	RowVersion  uint64
}

type AnalysisDecisionResult struct {
	Decision   AnalysisDecision
	Reason     string
	RowVersion uint64
}

// DecideAnalysis is deliberately conservative: uncertain or legacy content is
// never considered complete and therefore cannot suppress analysis.
func DecideAnalysis(snapshot AnalysisSnapshot) AnalysisDecisionResult {
	if !snapshot.Exists {
		return AnalysisDecisionResult{Decision: FullAnalysis, Reason: "row_absent"}
	}
	if snapshot.AnalyzedAt == nil {
		return AnalysisDecisionResult{Decision: FullAnalysis, Reason: "analyzed_at_missing", RowVersion: snapshot.RowVersion}
	}

	meta, metaOK := decodeJSONObject(snapshot.Meta)
	features, featuresOK := decodeJSONObject(snapshot.Features)
	predictions, predictionsOK := decodeJSONObject(snapshot.Predictions)
	if !metaOK || !featuresOK || !predictionsOK {
		return AnalysisDecisionResult{Decision: FullAnalysis, Reason: "invalid_json", RowVersion: snapshot.RowVersion}
	}
	version, ok := jsonInteger(meta["analysis_schema_version"])
	if !ok || version != AnalysisSchemaVersion {
		return AnalysisDecisionResult{Decision: FullAnalysis, Reason: "schema_version_mismatch", RowVersion: snapshot.RowVersion}
	}

	mix, mixOK := nonEmptyJSONObject(features["mix"])
	_ = mix
	mixComplete := mixOK && len(predictions) > 0
	stemsComplete := completeStemFeatures(features["demucs"])

	switch {
	case mixComplete && stemsComplete:
		return AnalysisDecisionResult{Decision: Skip, Reason: "complete", RowVersion: snapshot.RowVersion}
	case !mixComplete && stemsComplete:
		return AnalysisDecisionResult{Decision: MixOnly, Reason: "mix_incomplete", RowVersion: snapshot.RowVersion}
	case mixComplete && !stemsComplete:
		return AnalysisDecisionResult{Decision: StemsOnly, Reason: "stems_incomplete", RowVersion: snapshot.RowVersion}
	default:
		return AnalysisDecisionResult{Decision: FullAnalysis, Reason: "mix_and_stems_incomplete", RowVersion: snapshot.RowVersion}
	}
}

func decodeJSONObject(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, false
	}
	return value, true
}

func nonEmptyJSONObject(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok && len(object) > 0
}

func completeStemFeatures(value any) bool {
	stems, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for _, stem := range requiredAnalysisStems {
		if _, ok := nonEmptyJSONObject(stems[stem]); !ok {
			return false
		}
	}
	return true
}

func jsonInteger(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

func validateAnalysisDecision(decision AnalysisDecision) error {
	switch decision {
	case FullAnalysis, MixOnly, StemsOnly, Skip:
		return nil
	default:
		return fmt.Errorf("unknown analysis decision %q", decision)
	}
}
