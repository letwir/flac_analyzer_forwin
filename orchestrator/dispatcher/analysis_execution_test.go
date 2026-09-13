package dispatcher

import "testing"

func TestBuildAnalysisExecutionPlanStageMatrix(t *testing.T) {
	tests := []struct {
		decision AnalysisDecision
		want     AnalysisExecutionPlan
	}{
		{Skip, AnalysisExecutionPlan{Decision: Skip}},
		{MixOnly, AnalysisExecutionPlan{Decision: MixOnly, DecodeMix: true, AnalyzeMix: true, Tag: true, Ingest: true, AcquireLease: true}},
		{StemsOnly, AnalysisExecutionPlan{Decision: StemsOnly, DecodeMix: true, RunDemucs: true, AnalyzeStems: true, Tag: true, Ingest: true, AcquireLease: true}},
		{FullAnalysis, AnalysisExecutionPlan{Decision: FullAnalysis, DecodeMix: true, RunDemucs: true, AnalyzeMix: true, AnalyzeStems: true, Tag: true, Ingest: true, AcquireLease: true}},
	}
	for _, tt := range tests {
		t.Run(string(tt.decision), func(t *testing.T) {
			got, err := BuildAnalysisExecutionPlan(tt.decision)
			if err != nil || got != tt.want {
				t.Fatalf("plan=%+v err=%v want=%+v", got, err, tt.want)
			}
		})
	}
}

func TestBuildAnalysisExecutionPlanRejectsMissingPreflight(t *testing.T) {
	if _, err := BuildAnalysisExecutionPlan(""); err == nil {
		t.Fatal("missing preflight decision must fail closed")
	}
}
