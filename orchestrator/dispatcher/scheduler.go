package dispatcher

// WorkloadClass defines the categories of workload for the scheduler.
type WorkloadClass string

const (
	WorkloadClassMultiTrackCUE    WorkloadClass = "multi-track-cue"
	WorkloadClassLongSingleTrack  WorkloadClass = "long-single-track"
	WorkloadClassShortSingleTrack WorkloadClass = "short-single-track"
)

// ClassifyWorkload categorizes a task into its WorkloadClass based on track count and duration.
func ClassifyWorkload(task TaskPayload) WorkloadClass {
	trackCount := task.FileTrackCount
	if trackCount == 0 {
		trackCount = 1
	}
	if trackCount > 1 {
		return WorkloadClassMultiTrackCUE
	}

	sampleRate := task.SampleRate
	if sampleRate <= 0 {
		sampleRate = 44100
	}
	durationSec := 0.0
	if task.EndSample > task.StartSample {
		durationSec = float64(task.EndSample-task.StartSample) / float64(sampleRate)
	} else if task.FileSize > 0 {
		durationSec = float64(task.FileSize) / 176400.0
	}

	if durationSec >= 30*60 {
		return WorkloadClassLongSingleTrack
	}
	return WorkloadClassShortSingleTrack
}

// GetCPUConsumerCount determines the number of concurrent CPU stem consumers to use.
func GetCPUConsumerCount(task TaskPayload, poolMax int, stemCount int) int {
	class := ClassifyWorkload(task)
	if class == WorkloadClassShortSingleTrack {
		if poolMax < stemCount {
			return poolMax
		}
		return stemCount
	}
	return 1
}
