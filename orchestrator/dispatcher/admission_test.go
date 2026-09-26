package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"flac_analyzer/orchestrator/planner"
	"flac_analyzer/orchestrator/state"
	"flac_analyzer/orchestrator/sysinfo"
)

func testAdmissionCapacity() AdmissionCapacity {
	return AdmissionCapacity{
		HostRAMBytes:       8,
		DedicatedVRAMBytes: 6,
		DownstreamTracks:   1,
		CPULanes:           4,
		GPULanes:           2,
		DemucsSlots:        2,
		DiskTempBytes:      16,
	}
}

func testAdmissionPlan() AdmissionPlan {
	return AdmissionPlan{
		HostRAMBytes:       2,
		DedicatedVRAMBytes: 3,
		DownstreamTracks:   1,
		CPULanes:           1,
		GPULanes:           1,
		DemucsSlots:        1,
		DiskTempBytes:      4,
	}
}

func TestAdmissionPlanIsPureAndReportsAllResources(t *testing.T) {
	controller, err := NewAdmissionController(testAdmissionCapacity())
	if err != nil {
		t.Fatal(err)
	}
	request := testAdmissionPlan()
	decision := controller.Plan(request)
	if !decision.Admissible || decision.Request != request {
		t.Fatalf("unexpected admissibility decision: %+v", decision)
	}
	if usage := controller.Usage(); usage != (AdmissionUsage{}) {
		t.Fatalf("Plan mutated usage: %+v", usage)
	}

	lease, err := controller.AtomicReserve(request)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Token == 0 || lease.HostRAMBytes != request.HostRAMBytes || lease.DedicatedVRAMBytes != request.DedicatedVRAMBytes || lease.DownstreamTracks != request.DownstreamTracks || lease.CPULanes != request.CPULanes || lease.GPULanes != request.GPULanes || lease.DemucsSlots != request.DemucsSlots || lease.DiskTempBytes != request.DiskTempBytes {
		t.Fatalf("lease did not carry complete resource receipt: %+v", lease)
	}
	if !controller.Release(lease) || controller.Release(lease) {
		t.Fatal("Release must be idempotent")
	}
	if usage := controller.Usage(); usage != (AdmissionUsage{}) {
		t.Fatalf("resources leaked after release: %+v", usage)
	}
}

func TestAdmissionAtomicReserveNeverOvercommits(t *testing.T) {
	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes:       8,
		DedicatedVRAMBytes: 8,
		CPULanes:           4,
		GPULanes:           4,
		DemucsSlots:        4,
		DiskTempBytes:      8,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := AdmissionPlan{HostRAMBytes: 2, DedicatedVRAMBytes: 2, CPULanes: 1, GPULanes: 1, DemucsSlots: 1, DiskTempBytes: 2}

	const callers = 8
	start := make(chan struct{})
	leases := make(chan AdmissionLease, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			lease, reserveErr := controller.AtomicReserve(request)
			if reserveErr != nil {
				errs <- reserveErr
				return
			}
			leases <- lease
		})
	}
	close(start)
	wg.Wait()
	close(leases)
	close(errs)

	reservedLeases := make([]AdmissionLease, 0, 4)
	for lease := range leases {
		reservedLeases = append(reservedLeases, lease)
	}
	if len(reservedLeases) != 4 {
		t.Fatalf("reserved %d leases, want 4", len(reservedLeases))
	}
	for reserveErr := range errs {
		if !errors.Is(reserveErr, ErrAdmissionUnavailable) {
			t.Fatalf("unexpected reservation error: %v", reserveErr)
		}
	}
	if usage := controller.Usage(); usage.HostRAMBytes != 8 || usage.DedicatedVRAMBytes != 8 || usage.CPULanes != 4 || usage.GPULanes != 4 || usage.DemucsSlots != 4 || usage.DiskTempBytes != 8 {
		t.Fatalf("unexpected bounded usage: %+v", usage)
	}

	// A second concurrent release of every receipt is harmless and leaves no
	// reservation behind.
	for _, lease := range reservedLeases {
		var wg sync.WaitGroup
		results := make(chan bool, 2)
		for range 2 {
			wg.Go(func() { results <- controller.Release(lease) })
		}
		wg.Wait()
		close(results)
		var released int
		for result := range results {
			if result {
				released++
			}
		}
		if released != 1 {
			t.Fatalf("concurrent reservation release succeeded %d times, want 1", released)
		}
	}
	if usage := controller.Usage(); usage != (AdmissionUsage{}) {
		t.Fatalf("resources leaked after concurrent release: %+v", usage)
	}
}

func TestAdmissionDispatchReleasesOnCancelErrorAndPanic(t *testing.T) {
	controller, err := NewAdmissionController(testAdmissionCapacity())
	if err != nil {
		t.Fatal(err)
	}
	request := testAdmissionPlan()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := controller.Admit(ctx, request, func(context.Context, AdmissionLease) error {
		t.Fatal("cancelled dispatch callback was invoked")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled admission error = %v", err)
	}
	if usage := controller.Usage(); usage != (AdmissionUsage{}) {
		t.Fatalf("cancelled dispatch leaked resources: %+v", usage)
	}

	timedCtx, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	err = controller.Admit(timedCtx, request, func(ctx context.Context, _ AdmissionLease) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed dispatch error = %v", err)
	}
	if usage := controller.Usage(); usage != (AdmissionUsage{}) {
		t.Fatalf("timed dispatch leaked resources: %+v", usage)
	}

	dispatchErr := errors.New("dispatch failed")
	if err := controller.Admit(t.Context(), request, func(context.Context, AdmissionLease) error { return dispatchErr }); !errors.Is(err, dispatchErr) {
		t.Fatalf("dispatch error = %v", err)
	}
	if usage := controller.Usage(); usage != (AdmissionUsage{}) {
		t.Fatalf("failed dispatch leaked resources: %+v", usage)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_ = controller.Admit(t.Context(), request, func(context.Context, AdmissionLease) error { panic("synthetic dispatch panic") })
	}()
	if usage := controller.Usage(); usage != (AdmissionUsage{}) {
		t.Fatalf("panic dispatch leaked resources: %+v", usage)
	}
}

func TestAdmissionRejectsInvalidPlanWithoutMutation(t *testing.T) {
	controller, err := NewAdmissionController(testAdmissionCapacity())
	if err != nil {
		t.Fatal(err)
	}
	invalid := AdmissionPlan{DownstreamTracks: -1}
	decision := controller.Plan(invalid)
	if decision.Admissible || decision.Reason == "" {
		t.Fatalf("invalid plan was admitted: %+v", decision)
	}
	if _, err := controller.AtomicReserve(invalid); !errors.Is(err, ErrInvalidAdmissionPlan) {
		t.Fatalf("invalid reserve error = %v", err)
	}
	if usage := controller.Usage(); usage != (AdmissionUsage{}) {
		t.Fatalf("invalid plan mutated usage: %+v", usage)
	}
}

func TestAdmissionRAMShortageHoldsNoExecutionSlotsAndRecovers(t *testing.T) {
	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes: 4, DedicatedVRAMBytes: 8, CPULanes: 4,
		GPULanes: 2, DemucsSlots: 2, DiskTempBytes: 16,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Occupy 2 bytes
	occupier := AdmissionPlan{HostRAMBytes: 2}
	occupierLease, _ := controller.AtomicReserve(occupier)

	blocked := AdmissionPlan{HostRAMBytes: 3, DedicatedVRAMBytes: 2, CPULanes: 1, GPULanes: 1, DemucsSlots: 1}
	if _, err := controller.AtomicReserve(blocked); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("RAM-blocked reserve error = %v", err)
	}
	if usage := controller.Usage(); usage.GPULanes != 0 || usage.DemucsSlots != 0 || usage.CPULanes != 0 {
		t.Fatalf("RAM wait retained execution slots: %+v", usage)
	}

	small := AdmissionPlan{HostRAMBytes: 2, DedicatedVRAMBytes: 2, CPULanes: 1, GPULanes: 1, DemucsSlots: 1}
	lease, err := controller.AtomicReserve(small)
	if err != nil {
		t.Fatalf("small task did not progress: %v", err)
	}
	controller.Release(lease)
	controller.Release(occupierLease)

	if _, err := controller.AtomicReserve(blocked); err != nil {
		t.Fatalf("capacity should remain bounded after recovery, got %v", err)
	}
}

func TestMemoryPressureHysteresisAndGeneration(t *testing.T) {
	state := MemoryPressureState{EnterPercent: 90, ResumePercent: 82}
	state, decision := EvaluateMemoryPressurePure(state, 90, true)
	if !decision.Entered || !decision.Active || !decision.ForceDisk || decision.Generation != 1 {
		t.Fatalf("enter decision = %+v", decision)
	}
	state, decision = EvaluateMemoryPressurePure(state, 85, true)
	if decision.Entered || decision.Recovered || !decision.Active || decision.Generation != 1 {
		t.Fatalf("hysteresis decision = %+v", decision)
	}
	state, decision = EvaluateMemoryPressurePure(state, 82, true)
	if !decision.Recovered || decision.Active || decision.Generation != 1 {
		t.Fatalf("recovery decision = %+v", decision)
	}
	_, decision = EvaluateMemoryPressurePure(state, 91, false)
	if !decision.Active || decision.ForceDisk {
		t.Fatalf("fallback-disabled decision = %+v", decision)
	}
}

func TestDurableFeederParksBlockedTasksWithoutOccupyingWorkers(t *testing.T) {
	db, err := state.InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()
	completed := make(chan TaskPayload, 4)
	d := &Dispatcher{
		config:         Config{GatekeeperRetryDelaySec: 60},
		db:             db,
		taskQueue:      make(chan TaskPayload),
		workerReadyCh:  make(chan struct{}, 4),
		taskFeederCtx:  workerCtx,
		parkLogReasons: make(map[string]time.Time),
		prepareAnalysisFn: func(_ context.Context, tasks []TaskPayload) ([]TaskPayload, error) {
			for i := range tasks {
				tasks[i].AnalysisDecision = FullAnalysis
			}
			return tasks, nil
		},
		reserveTaskFn: func(task TaskPayload) (AdmissionLease, error) {
			if task.FileSize >= 1000 {
				return AdmissionLease{}, ErrAdmissionUnavailable
			}
			return AdmissionLease{Token: uint64(task.TrackNumber)}, nil
		},
		executeTaskFn: func(_ int, task TaskPayload) { completed <- task },
	}
	for i := 1; i <= 8; i++ {
		size := int64(10)
		if i <= 4 {
			size = 10_000
		}
		path := fmt.Sprintf("C:/music/task-%d.flac", i)
		payload := fmt.Sprintf(`{"flacPath":%q,"trackNumber":%d,"fileSize":%d}`, path, i, size)
		if _, err := db.CheckOrInsertWithPayload(path, i, payload, false); err != nil {
			t.Fatal(err)
		}
	}
	for id := 1; id <= 4; id++ {
		d.wg.Add(1)
		go d.worker(id)
	}
	d.fillTaskQueue(4)
	stopWorkers()
	close(d.taskQueue)
	d.wg.Wait()
	close(completed)
	if got := len(completed); got != 4 {
		t.Fatalf("completed small tasks=%d want=4", got)
	}
	if got := atomic.LoadInt32(&d.activeTaskCount); got != 0 {
		t.Fatalf("blocked tasks occupied %d workers", got)
	}
}

func TestDurableFeederResumesAfterDownstreamTicketReturns(t *testing.T) {
	db, err := state.InitDB(filepath.Join(t.TempDir(), "orchestrator.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	controller, err := NewAdmissionController(AdmissionCapacity{DownstreamTracks: 1})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := controller.AtomicReserve(AdmissionPlan{DownstreamTracks: 1})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "queued.flac")
	payload := fmt.Sprintf(`{"flacPath":%q,"trackNumber":1,"fileSize":100}`, path)
	if _, err := db.CheckOrInsertWithPayload(path, 1, payload, false); err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{
		config:         Config{GatekeeperRetryDelaySec: 1},
		db:             db,
		taskQueue:      make(chan TaskPayload, 1),
		taskFeederCtx:  context.Background(),
		parkLogReasons: make(map[string]time.Time),
		prepareAnalysisFn: func(_ context.Context, tasks []TaskPayload) ([]TaskPayload, error) {
			for i := range tasks {
				tasks[i].AnalysisDecision = FullAnalysis
			}
			return tasks, nil
		},
		reserveTaskFn: func(TaskPayload) (AdmissionLease, error) {
			return controller.AtomicReserve(AdmissionPlan{DownstreamTracks: 1})
		},
	}
	d.fillTaskQueue(1)
	if len(d.taskQueue) != 0 || atomic.LoadInt32(&d.activeTaskCount) != 0 {
		t.Fatal("ticket-blocked task occupied a worker or reached the task queue")
	}
	parked, err := db.GetTaskState(path, 1)
	if err != nil || parked.Status != state.StatusFailedMaybeRetry {
		t.Fatalf("blocked task state=%+v err=%v", parked, err)
	}
	if !controller.Release(blocker) {
		t.Fatal("failed to return blocker ticket")
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(d.taskQueue) == 0 && time.Now().Before(deadline) {
		d.fillTaskQueue(1)
		time.Sleep(50 * time.Millisecond)
	}
	if len(d.taskQueue) != 1 {
		t.Fatal("task did not resume after ticket returned and durable retry delay elapsed")
	}
	if atomic.LoadInt32(&d.activeTaskCount) != 0 {
		t.Fatal("feeder wait occupied a worker")
	}
}

func TestParkReasonLoggingIsAggregated(t *testing.T) {
	d := &Dispatcher{parkLogReasons: make(map[string]time.Time)}
	now := time.Unix(100, 0)
	if !d.shouldLogParkReason("low RAM", now) || d.shouldLogParkReason("low RAM", now.Add(30*time.Second)) {
		t.Fatal("duplicate reason was not suppressed")
	}
	if !d.shouldLogParkReason("low RAM", now.Add(time.Minute)) {
		t.Fatal("reason did not become loggable after aggregation window")
	}
}

func TestAdmissionPermanentBudgetExcess(t *testing.T) {
	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Too big to ever fit
	largePlan := AdmissionPlan{HostRAMBytes: 200}

	_, err = controller.AtomicReserve(largePlan)
	if !errors.Is(err, ErrPermanentBudgetExcess) {
		t.Fatalf("expected ErrPermanentBudgetExcess, got %v", err)
	}
}

func TestAdmissionTemporaryDiskAndVRAMShortageDoesNotBecomePermanent(t *testing.T) {
	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes: 100, DedicatedVRAMBytes: 10, DiskTempBytes: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.AtomicReserve(AdmissionPlan{DedicatedVRAMBytes: 11, DiskTempBytes: 11})
	if !errors.Is(err, ErrAdmissionUnavailable) || errors.Is(err, ErrPermanentBudgetExcess) {
		t.Fatalf("temporary observed-space shortage was misclassified: %v", err)
	}
}

func TestAdmissionPredecessorWavefrontBlocksNextDemucs(t *testing.T) {
	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes: 100, DedicatedVRAMBytes: 100, CPULanes: 4, GPULanes: 4,
		DownstreamTracks: 1, DemucsSlots: 1, DiskTempBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	predecessorTicket := AdmissionPlan{HostRAMBytes: 1, DownstreamTracks: 1}
	ticket, err := controller.AtomicReserve(predecessorTicket)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Release(ticket)

	// The predecessor's Demucs execution slot is free while its wavefront ticket remains held.
	demucsLease, err := controller.AtomicReserve(AdmissionPlan{DemucsSlots: 1})
	if err != nil {
		t.Fatalf("predecessor Demucs execution did not fit: %v", err)
	}
	controller.Release(demucsLease)

	nextTicket := AdmissionPlan{HostRAMBytes: 1, DownstreamTracks: 1}
	_, err = controller.AtomicReserve(nextTicket)
	if !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("expected ErrAdmissionUnavailable, got %v", err)
	}

	controller.Release(ticket)
	_, err = controller.AtomicReserve(nextTicket)
	if err != nil {
		t.Fatalf("expected next task to proceed, got %v", err)
	}
}

func TestAdmissionTicketWaitsForCleanupBeforeReleaseOnCleanupError(t *testing.T) {
	controller, err := NewAdmissionController(AdmissionCapacity{DownstreamTracks: 1})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := controller.AtomicReserve(AdmissionPlan{DownstreamTracks: 1})
	if err != nil {
		t.Fatal(err)
	}
	cleanupErr := errors.New("synthetic SHM cleanup failure")
	released := false
	gotErr := cleanupThenRelease(func() error {
		if got := controller.Usage().DownstreamTracks; got != 1 {
			t.Fatalf("ticket released before cleanup: used=%d", got)
		}
		return cleanupErr
	}, func() {
		released = controller.Release(lease)
	})
	if !errors.Is(gotErr, cleanupErr) || !released {
		t.Fatalf("cleanup/release result err=%v released=%v", gotErr, released)
	}
	if got := controller.Usage().DownstreamTracks; got != 0 {
		t.Fatalf("ticket remained after cleanup failure: used=%d", got)
	}
}

func TestUnknownSharedGPUMemoryFailsClosed(t *testing.T) {
	now := time.Now()
	valid := &sysinfo.GpuMetrics{CollectedAt: now, SharedUsageValid: true}
	if err := validateSharedGPUMemoryObservation(valid, now); err != nil {
		t.Fatalf("valid shared-memory observation rejected: %v", err)
	}
	if err := validateSharedGPUMemoryObservation(nil, now); err == nil {
		t.Fatal("missing GPU observation was admitted")
	}
	unknown := *valid
	unknown.SharedUsageValid = false
	if err := validateSharedGPUMemoryObservation(&unknown, now); err == nil {
		t.Fatal("unknown shared-memory use was admitted")
	}
	stale := *valid
	stale.CollectedAt = now.Add(-2 * sysinfo.DefaultGpuStaleThreshold)
	if err := validateSharedGPUMemoryObservation(&stale, now); err == nil {
		t.Fatal("stale shared-memory use was admitted")
	}
}

func TestAdmissionEstimateUsesDecisionStemsAndLongTrackCPUParallelism(t *testing.T) {
	base := TaskPayload{FileSize: 100_000_000, SampleRate: 44100}
	base.AnalysisDecision = MixOnly
	mix, mixCPU, err := estimateTaskAdmissionResources(base, 4, planner.DefaultResourceProfile())
	if err != nil {
		t.Fatal(err)
	}
	base.AnalysisDecision = FullAnalysis
	full, fullCPU, err := estimateTaskAdmissionResources(base, 4, planner.DefaultResourceProfile())
	if err != nil {
		t.Fatal(err)
	}
	if mix.StemBufferBytes >= full.StemBufferBytes || mixCPU != 1 || fullCPU != 4 {
		t.Fatalf("MixOnly/full estimates or CPU consumers wrong: mix=%+v/%d full=%+v/%d", mix, mixCPU, full, fullCPU)
	}
	base.EndSample = int64(44100 * 60 * 31)
	base.AnalysisDecision = FullAnalysis
	long, consumers, err := estimateTaskAdmissionResources(base, 4, planner.DefaultResourceProfile())
	if err != nil {
		t.Fatal(err)
	}
	if consumers != 1 || long.CPUWorkingRamBytes == 0 {
		t.Fatalf("long-track CPU profile=%+v consumers=%d", long, consumers)
	}
}
