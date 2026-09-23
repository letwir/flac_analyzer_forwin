package dispatcher

import (
	"testing"
)

func TestClassifyWorkload(t *testing.T) {
	tests := []struct {
		name     string
		task     TaskPayload
		expected WorkloadClass
	}{
		{
			name:     "multi-track CUE",
			task:     TaskPayload{FileTrackCount: 5},
			expected: WorkloadClassMultiTrackCUE,
		},
		{
			name:     "short single-track",
			task:     TaskPayload{FileTrackCount: 1, StartSample: 0, EndSample: 44100 * 60, SampleRate: 44100}, // 1 minute
			expected: WorkloadClassShortSingleTrack,
		},
		{
			name:     "long single-track",
			task:     TaskPayload{FileTrackCount: 1, StartSample: 0, EndSample: 44100 * 60 * 35, SampleRate: 44100}, // 35 minutes
			expected: WorkloadClassLongSingleTrack,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyWorkload(tt.task)
			if got != tt.expected {
				t.Errorf("ClassifyWorkload() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestGetCPUConsumerCount(t *testing.T) {
	poolMax := 4
	stemCount := 6

	taskMulti := TaskPayload{FileTrackCount: 5}
	if got := GetCPUConsumerCount(taskMulti, poolMax, stemCount); got != 1 {
		t.Errorf("GetCPUConsumerCount(multi) = %d, want 1", got)
	}

	taskLong := TaskPayload{FileTrackCount: 1, StartSample: 0, EndSample: 44100 * 60 * 35, SampleRate: 44100}
	if got := GetCPUConsumerCount(taskLong, poolMax, stemCount); got != 1 {
		t.Errorf("GetCPUConsumerCount(long) = %d, want 1", got)
	}

	taskShort := TaskPayload{FileTrackCount: 1, StartSample: 0, EndSample: 44100 * 60, SampleRate: 44100}
	if got := GetCPUConsumerCount(taskShort, poolMax, stemCount); got != poolMax {
		t.Errorf("GetCPUConsumerCount(short) = %d, want %d", got, poolMax)
	}
}
