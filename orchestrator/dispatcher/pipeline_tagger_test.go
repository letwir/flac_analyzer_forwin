package dispatcher

import (
	"path/filepath"
	"testing"
)

func TestTaggerFailurePropagates(t *testing.T) {
	d := &Dispatcher{config: Config{QueueDir: t.TempDir()}}
	// Real Python tagger must fail before a missing FLAC can be marked complete.
	for _, raw := range []string{`{}`, `{"mix":{"scalars":{"bpm":128}}}`} {
		task := TaskPayload{FlacPath: filepath.Join(t.TempDir(), "missing.flac"), TrackNumber: 1}
		err := d.executeTaggerStage(0, task, "test-tagger-failure", &FeatureOutputs{
			LibOut: raw, EssOut: `{}`, TensorOut: `{}`,
		})
		if err == nil {
			t.Fatal("tagger failure was swallowed")
		}
	}
}
