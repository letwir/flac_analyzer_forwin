package dispatcher

import "fmt"

type AnalysisExecutionPlan struct {
	Decision     AnalysisDecision
	DecodeMix    bool
	RunDemucs    bool
	AnalyzeMix   bool
	AnalyzeStems bool
	Tag          bool
	Ingest       bool
	AcquireLease bool
}

func BuildAnalysisExecutionPlan(decision AnalysisDecision) (AnalysisExecutionPlan, error) {
	if err := validateAnalysisDecision(decision); err != nil {
		return AnalysisExecutionPlan{}, err
	}
	plan := AnalysisExecutionPlan{Decision: decision}
	switch decision {
	case Skip:
		return plan, nil
	case MixOnly:
		plan.DecodeMix, plan.AnalyzeMix = true, true
	case StemsOnly:
		plan.DecodeMix, plan.RunDemucs, plan.AnalyzeStems = true, true, true
	case FullAnalysis:
		plan.DecodeMix, plan.RunDemucs, plan.AnalyzeMix, plan.AnalyzeStems = true, true, true, true
	default:
		return AnalysisExecutionPlan{}, fmt.Errorf("unreachable analysis decision %q", decision)
	}
	plan.Tag, plan.Ingest, plan.AcquireLease = true, true, true
	return plan, nil
}

func stemsForAnalysisPlan(plan AnalysisExecutionPlan) []string {
	stems := []string{"bass", "drums", "vocals", "other", "guitar", "piano"}
	if plan.AnalyzeMix {
		return append([]string{"mix"}, stems...)
	}
	return stems
}
