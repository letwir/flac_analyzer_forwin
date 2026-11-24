package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

func TestValidateDiskModeCapacityOnPromotionRevalidatesUnknownAndLowDisk(t *testing.T) {
	// Unknown disk size must fail when promoted to StorageModeDisk even if sentinel MaxUint64 was held.
	err := validateDiskModeCapacityPure(StorageModeDisk, false, ^uint64(0), 2<<30, 5<<30)
	if err == nil || !strings.Contains(err.Error(), "disk observation unavailable") {
		t.Fatalf("expected disk observation unavailable on unknown disk promotion, got %v", err)
	}

	// Insufficient disk capacity below minAvailDisk + diskBytes must fail.
	err = validateDiskModeCapacityPure(StorageModeDisk, true, 6<<30, 2<<30, 5<<30)
	if err == nil || !strings.Contains(err.Error(), "insufficient disk space") {
		t.Fatalf("expected insufficient disk space error, got %v", err)
	}

	// Sufficient known disk capacity succeeds.
	if err := validateDiskModeCapacityPure(StorageModeDisk, true, 8<<30, 2<<30, 5<<30); err != nil {
		t.Fatalf("unexpected error with sufficient known disk space: %v", err)
	}

	// SHM mode ignores disk fallback gate.
	if err := validateDiskModeCapacityPure(StorageModeSHM, false, ^uint64(0), 0, 5<<30); err != nil {
		t.Fatalf("unexpected error in SHM mode: %v", err)
	}

	// Permanent RAM rejection must NOT be masked by low/unknown disk during SHM->Disk promotion.
	est := planner.ResourceEstimate{DiskModeRamBytes: 10 << 30, DiskBytes: 2 << 30}
	_, _, _, err = applyPromotionAndValidateDiskPure(
		StorageModeSHM,
		est,
		1<<30,
		12<<30,
		0,
		8<<30, // Promoted RAM (11 GiB) still > 8 GiB budget
		1<<30, // Low disk
		5<<30,
		false, // Unknown disk
		true,
	)
	if !errors.Is(err, ErrPermanentBudgetExcess) {
		t.Fatalf("expected ErrPermanentBudgetExcess to take precedence over disk check, got %v", err)
	}

	// Forced Disk mode (pressure.ForceDisk) with unknown disk AND over-budget RAM must still surface ErrPermanentBudgetExcess.
	_, _, _, err = applyPromotionAndValidateDiskPure(
		StorageModeDisk,
		est,
		1<<30,
		11<<30, // Already switched to StorageModeDisk by pressure.ForceDisk, still > 8 GiB budget
		est.DiskBytes,
		8<<30,
		^uint64(0),
		5<<30,
		false, // Unknown disk must not mask permanent RAM excess
		true,
	)
	if !errors.Is(err, ErrPermanentBudgetExcess) {
		t.Fatalf("expected ErrPermanentBudgetExcess on ForceDisk + unknown disk + over-budget RAM, got %v", err)
	}

	// Non-Disk mode with diskBytes > 0 must reject unknown disk so MaxUint64 never leaks into capacity.
	_, _, _, err = applyPromotionAndValidateDiskPure(
		StorageModeSHM,
		est,
		1<<30,
		4<<30,
		1<<30,
		8<<30,
		^uint64(0),
		5<<30,
		false,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "disk observation unavailable") {
		t.Fatalf("expected disk observation unavailable when non-Disk plan carries diskBytes > 0 and unknown disk, got %v", err)
	}

	// Full promotion path where promoted RAM fits <= budget: must enforce unknown / insufficient / sufficient disk checks.
	fitEst := planner.ResourceEstimate{DiskModeRamBytes: 5 << 30, DiskBytes: 2 << 30}
	_, _, _, err = applyPromotionAndValidateDiskPure(
		StorageModeSHM,
		fitEst,
		1<<30,
		10<<30, // > 8 GiB budget -> promotes to Disk (5+1 = 6 <= 8 GiB)
		0,
		8<<30,
		^uint64(0),
		5<<30,
		false, // Unknown disk
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "disk observation unavailable") {
		t.Fatalf("expected unknown disk error after fitting promotion, got %v", err)
	}

	_, _, _, err = applyPromotionAndValidateDiskPure(
		StorageModeSHM,
		fitEst,
		1<<30,
		10<<30,
		0,
		8<<30,
		6<<30, // < 5 + 2 = 7 GiB required
		5<<30,
		true,
		true,
	)
	if err == nil || !strings.Contains(err.Error(), "insufficient disk space") {
		t.Fatalf("expected insufficient disk error after fitting promotion, got %v", err)
	}

	promotedMode, promotedRAM, promotedDisk, err := applyPromotionAndValidateDiskPure(
		StorageModeSHM,
		fitEst,
		1<<30,
		10<<30,
		0,
		8<<30,
		8<<30, // >= 5 + 2 = 7 GiB required
		5<<30,
		true,
		true,
	)
	if err != nil {
		t.Fatalf("expected fitting promotion with sufficient disk space to succeed, got %v", err)
	}
	if promotedMode != StorageModeDisk || promotedRAM != 6<<30 || promotedDisk != fitEst.DiskBytes {
		t.Fatalf("unexpected promoted plan: mode=%s ram=%d disk=%d", promotedMode, promotedRAM, promotedDisk)
	}
}

func TestPromotedDiskTaskLeasePreservesDiskTempBytesOnLastMileRecheck(t *testing.T) {
	fitEst := planner.ResourceEstimate{DiskModeRamBytes: 5 << 30, DiskBytes: 2 << 30}
	promotedMode, promotedRAM, promotedDisk, err := applyPromotionAndValidateDiskPure(
		StorageModeSHM,
		fitEst,
		1<<30,
		10<<30, // Exceeds 8 GiB RAM budget -> promotes to Disk
		0,
		8<<30,
		8<<30, // 8 GiB free disk >= 5 GiB floor + 2 GiB estimate
		5<<30,
		true,
		true,
	)
	if err != nil {
		t.Fatalf("expected SHM->Disk promotion to succeed with 8 GiB free disk, got %v", err)
	}

	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes:     16 << 30,
		DownstreamTracks: 1,
		DiskTempBytes:    16 << 30,
	})
	if err != nil {
		t.Fatalf("NewAdmissionController failed: %v", err)
	}
	lease, err := controller.AtomicReserve(AdmissionPlan{
		HostRAMBytes:     promotedRAM,
		DiskTempBytes:    promotedDisk,
		DownstreamTracks: 1,
	})
	if err != nil {
		t.Fatalf("AtomicReserve failed: %v", err)
	}
	defer controller.Release(lease)
	lease.StorageMode = promotedMode
	if lease.DiskTempBytes != fitEst.DiskBytes || lease.DiskTempBytes == 0 {
		t.Fatalf("expected reserved lease DiskTempBytes=%d, got %d", fitEst.DiskBytes, lease.DiskTempBytes)
	}

	cfg := Config{
		MinAvailRamGB:          2,
		MinAvailDiskGB:         5,
		MinAvailVramGB:         1,
		EstimatedDemucsVramGB:  1,
		MaxGpuUtilizationRatio: 0.9,
		GatekeeperRetryDelaySec: 5,
	}
	memInfo := &sysinfo.MemoryInfo{TotalPhys: 32 << 30, AvailPhys: 16 << 30, MemoryLoad: 50}
	gpu := &sysinfo.GpuMetrics{
		DedicatedTotalBytes:     8 << 30,
		AvailableVramBytes:      6 << 30,
		DedicatedCapacityValid:  true,
		DedicatedAvailableValid: true,
		SharedUsageValid:        true,
		CollectedAt:             time.Now(),
	}

	// Initially sufficient free disk (8 GiB >= 5 GiB floor + 2 GiB DiskTempBytes) passes last-mile check.
	initial := EvaluateGoNoGoPure(buildLastMileGatekeeperInputPure(cfg, memInfo, gpu, 8<<30, true, MemoryPressureDecision{}, lease))
	if !initial.IsGo {
		t.Fatalf("expected initial last-mile recheck to pass with 8 GiB disk, got reason=%s", initial.Reason)
	}

	// Free disk drops to 6 GiB (> 5 GiB floor alone, < 5 GiB floor + 2 GiB DiskTempBytes): MUST reject!
	lowered := EvaluateGoNoGoPure(buildLastMileGatekeeperInputPure(cfg, memInfo, gpu, 6<<30, true, MemoryPressureDecision{}, lease))
	if lowered.IsGo {
		t.Fatalf("expected last-mile recheck to reject promoted Disk task at 6 GiB avail (< 5 GiB floor + %d B DiskTempBytes), got GO", lease.DiskTempBytes)
	}
}

func TestResolveDownstreamTracksCapPure(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	cases := []struct {
		name          string
		configuredMax int
		numWorkers    int
		ramBudget     uint64
		perTrackRAM   uint64
		want          int
	}{
		{"explicit_1", 1, 16, 64 * gib, 4 * gib, 1},
		{"explicit_4", 4, 2, 16 * gib, 4 * gib, 4},
		{"auto_high_ram_16_workers", 0, 16, 64 * gib, 4 * gib, 4},
		{"auto_low_ram_2_tracks", 0, 16, 9 * gib, 4 * gib, 2},
		{"auto_single_worker", 0, 1, 64 * gib, 4 * gib, 1},
		{"auto_zero_workers_clamps_to_1", 0, 0, 64 * gib, 4 * gib, 1},
		{"auto_insufficient_ram_clamps_to_1", 0, 8, 2 * gib, 4 * gib, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveDownstreamTracksCapPure(tc.configuredMax, tc.numWorkers, tc.ramBudget, tc.perTrackRAM)
			if got != tc.want {
				t.Fatalf("ResolveDownstreamTracksCapPure(%d, %d, %d, %d) = %d, want %d",
					tc.configuredMax, tc.numWorkers, tc.ramBudget, tc.perTrackRAM, got, tc.want)
			}
		})
	}
}

func TestAdmissionControllerThreeTierLedgerSeparation(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes:       32 * gib,
		DedicatedVRAMBytes: 12 * gib,
		DownstreamTracks:   4,
		CPULanes:           8,
		GPULanes:           1,
		DemucsSlots:        1,
		DiskTempBytes:      64 * gib,
	})
	if err != nil {
		t.Fatalf("NewAdmissionController failed: %v", err)
	}

	downLease, err := controller.ReserveTier(AdmissionTierDownstream, AdmissionPlan{
		HostRAMBytes:     4 * gib,
		DownstreamTracks: 1,
		DiskTempBytes:    2 * gib,
	})
	if err != nil {
		t.Fatalf("ReserveTier(Downstream) failed: %v", err)
	}
	if downLease.Tier != AdmissionTierDownstream {
		t.Fatalf("expected tier Downstream, got %v", downLease.Tier)
	}

	gpuLease, err := controller.ReserveTier(AdmissionTierGPUExecution, AdmissionPlan{
		DedicatedVRAMBytes: 4 * gib,
		CPULanes:           1,
		GPULanes:           1,
		DemucsSlots:        1,
	})
	if err != nil {
		t.Fatalf("ReserveTier(GPUExecution) failed: %v", err)
	}
	if gpuLease.Tier != AdmissionTierGPUExecution {
		t.Fatalf("expected tier GPUExecution, got %v", gpuLease.Tier)
	}

	retainedLease, err := controller.ReserveTier(AdmissionTierRetainedResidency, AdmissionPlan{
		DedicatedVRAMBytes: 2 * gib,
	})
	if err != nil {
		t.Fatalf("ReserveTier(RetainedResidency) failed: %v", err)
	}
	if retainedLease.Tier != AdmissionTierRetainedResidency {
		t.Fatalf("expected tier RetainedResidency, got %v", retainedLease.Tier)
	}

	if got := controller.ActiveLeasesByTier(AdmissionTierDownstream); got != 1 {
		t.Fatalf("Downstream active leases = %d, want 1", got)
	}
	if got := controller.ActiveLeasesByTier(AdmissionTierGPUExecution); got != 1 {
		t.Fatalf("GPUExecution active leases = %d, want 1", got)
	}
	if got := controller.ActiveLeasesByTier(AdmissionTierRetainedResidency); got != 1 {
		t.Fatalf("RetainedResidency active leases = %d, want 1", got)
	}

	// Releasing GPUExecution must not disturb Downstream or RetainedResidency.
	if !controller.Release(gpuLease) {
		t.Fatal("expected Release(gpuLease) to succeed")
	}
	if got := controller.UsageByTier(AdmissionTierGPUExecution).DedicatedVRAMBytes; got != 0 {
		t.Fatalf("GPUExecution VRAM after release = %d, want 0", got)
	}
	if got := controller.UsageByTier(AdmissionTierDownstream).HostRAMBytes; got != 4*gib {
		t.Fatalf("Downstream HostRAM after GPU release = %d, want %d", got, 4*gib)
	}
	if got := controller.UsageByTier(AdmissionTierRetainedResidency).DedicatedVRAMBytes; got != 2*gib {
		t.Fatalf("RetainedResidency VRAM after GPU release = %d, want %d", got, 2*gib)
	}

	// Drain remaining tiers.
	if !controller.Release(downLease) || !controller.Release(retainedLease) {
		t.Fatal("expected remaining tier releases to succeed")
	}
	for _, tier := range []AdmissionTier{AdmissionTierDownstream, AdmissionTierGPUExecution, AdmissionTierRetainedResidency} {
		if got := controller.ActiveLeasesByTier(tier); got != 0 {
			t.Fatalf("tier %v active leases after drain = %d, want 0", tier, got)
		}
		if u := controller.UsageByTier(tier); u != (AdmissionUsage{}) {
			t.Fatalf("tier %v usage after drain = %+v, want zero", tier, u)
		}
	}
	if u := controller.Usage(); u != (AdmissionUsage{}) {
		t.Fatalf("aggregate usage after drain = %+v, want zero", u)
	}
}

func TestAdmissionQueueVsGPULaunchSeparation(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	in := GatekeeperInput{
		StorageMode:         StorageModeSHM,
		AvailPhys:           32 * gib,
		EstimatedTaskRam:    4 * gib,
		MinAvailRam:         2 * gib,
		MemoryLoad:          40,
		AvailDisk:           50 * gib,
		MinAvailDisk:        5 * gib,
		GpuUtilization:      95.0,
		AvailVram:           0,
		MinAvailVram:        1 * gib,
		EstimatedTaskVram:   2 * gib,
		GPURequired:         true,
		DedicatedVramKnown:  false,
		DedicatedVramStatus: "busy",
		MaxGpuUtilization:   0.85,
		EnableGpuThrottle:   true,
		RetryDelay:          5 * time.Second,
	}

	queueDec := EvaluateQueueAdmissionPure(in)
	if !queueDec.IsGo {
		t.Fatalf("expected EvaluateQueueAdmissionPure to admit when host RAM/disk are healthy despite busy GPU, got reason=%s", queueDec.Reason)
	}

	gpuDec := EvaluateGPULaunchPure(in)
	if gpuDec.IsGo || !gpuDec.IsGpuBlock {
		t.Fatalf("expected EvaluateGPULaunchPure to block on busy/unknown GPU, got IsGo=%v IsGpuBlock=%v reason=%s", gpuDec.IsGo, gpuDec.IsGpuBlock, gpuDec.Reason)
	}
}

func TestAdmissionDownstreamTracks4AndDrain(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes:     32 * gib,
		DownstreamTracks: 4,
		DiskTempBytes:    32 * gib,
	})
	if err != nil {
		t.Fatalf("NewAdmissionController failed: %v", err)
	}

	var leases []AdmissionLease
	for i := 0; i < 4; i++ {
		lease, reserveErr := controller.ReserveTier(AdmissionTierDownstream, AdmissionPlan{
			HostRAMBytes:     4 * gib,
			DownstreamTracks: 1,
		})
		if reserveErr != nil {
			t.Fatalf("expected downstream track %d to reserve, got %v", i+1, reserveErr)
		}
		leases = append(leases, lease)
	}

	if _, fifthErr := controller.ReserveTier(AdmissionTierDownstream, AdmissionPlan{
		HostRAMBytes:     4 * gib,
		DownstreamTracks: 1,
	}); !errors.Is(fifthErr, ErrAdmissionUnavailable) {
		t.Fatalf("expected 5th downstream track to fail with ErrAdmissionUnavailable, got %v", fifthErr)
	}

	for _, lease := range leases {
		if !controller.Release(lease) {
			t.Fatalf("expected release of lease token %d to succeed", lease.Token)
		}
	}
	if got := controller.Usage().DownstreamTracks; got != 0 {
		t.Fatalf("DownstreamTracks after drain = %d, want 0", got)
	}
	if got := controller.ActiveLeasesByTier(AdmissionTierDownstream); got != 0 {
		t.Fatalf("ActiveLeasesByTier(Downstream) after drain = %d, want 0", got)
	}
}

func TestEvaluateExecutionGPULaunchPure_InsufficientFreeVRAM(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	controller, err := NewAdmissionController(AdmissionCapacity{
		DedicatedVRAMBytes: 8 * gib,
		CPULanes:           4,
		GPULanes:           1,
		DemucsSlots:        1,
	})
	if err != nil {
		t.Fatalf("NewAdmissionController failed: %v", err)
	}

	d := &Dispatcher{
		config: Config{
			MinAvailVramGB:         1.0,
			EstimatedDemucsVramGB:  1.5,
			MaxGpuUtilizationRatio: 0.85,
			EnableGpuThrottle:      true,
			GatekeeperRetryDelaySec: 5,
		},
		admission: controller,
	}

	now := time.Now()
	lowFreeGPU := &sysinfo.GpuMetrics{
		DedicatedTotalBytes:     8 * gib,
		AvailableVramBytes:      2 * gib, // Known free VRAM < 1.5 GiB task + 1.0 GiB floor = 2.5 GiB
		DedicatedCapacityValid:  true,
		DedicatedAvailableValid: true,
		SharedUsageValid:        true,
		UtilizationPercent:      20.0,
		CollectedAt:             now,
	}
	if _, err := d.reserveExecutionAdmissionWithGPU(lowFreeGPU, now); err == nil {
		t.Fatal("expected reserveExecutionAdmissionWithGPU to reject known insufficient free VRAM")
	}
	if got := controller.ActiveLeasesByTier(AdmissionTierGPUExecution); got != 0 {
		t.Fatalf("expected 0 GPUExecution leases on rejected launch, got %d", got)
	}

	// Even with EnableGpuThrottle=false, known insufficient free VRAM must reject launch.
	d.config.EnableGpuThrottle = false
	zeroFreeGPU := &sysinfo.GpuMetrics{
		DedicatedTotalBytes:     8 * gib,
		AvailableVramBytes:      0,
		DedicatedCapacityValid:  true,
		DedicatedAvailableValid: true,
		SharedUsageValid:        true,
		UtilizationPercent:      10.0,
		CollectedAt:             now,
	}
	if _, err := d.reserveExecutionAdmissionWithGPU(zeroFreeGPU, now); err == nil {
		t.Fatal("expected reserveExecutionAdmissionWithGPU to reject 0 free VRAM even when EnableGpuThrottle=false")
	}
	if _, err := d.reserveExecutionAdmissionWithGPU(lowFreeGPU, now); err == nil {
		t.Fatal("expected reserveExecutionAdmissionWithGPU to reject insufficient free VRAM even when EnableGpuThrottle=false")
	}
	d.config.EnableGpuThrottle = true

	healthyGPU := &sysinfo.GpuMetrics{
		DedicatedTotalBytes:     8 * gib,
		AvailableVramBytes:      4 * gib,
		DedicatedCapacityValid:  true,
		DedicatedAvailableValid: true,
		SharedUsageValid:        true,
		UtilizationPercent:      20.0,
		CollectedAt:             now,
	}
	lease, err := d.reserveExecutionAdmissionWithGPU(healthyGPU, now)
	if err != nil {
		t.Fatalf("expected healthy GPU launch to succeed, got %v", err)
	}
	if got := controller.ActiveLeasesByTier(AdmissionTierGPUExecution); got != 1 {
		t.Fatalf("expected 1 GPUExecution lease, got %d", got)
	}
	d.releaseExecutionAdmission(lease)
	if got := controller.ActiveLeasesByTier(AdmissionTierGPUExecution); got != 0 {
		t.Fatalf("expected 0 GPUExecution leases after release, got %d", got)
	}
}

func TestAdmissionMixedSizeCandidatePreservesSharedCap(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	budget := 32 * gib
	stableTrackRAM := 2 * gib
	sharedCap := ResolveDownstreamTracksCapPure(0, 4, budget, stableTrackRAM)
	if sharedCap != 4 {
		t.Fatalf("expected sharedCap=4, got %d", sharedCap)
	}

	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes:     budget,
		DownstreamTracks: sharedCap,
		DiskTempBytes:    64 * gib,
	})
	if err != nil {
		t.Fatalf("NewAdmissionController failed: %v", err)
	}

	lease1, err := controller.ReserveTier(AdmissionTierDownstream, AdmissionPlan{HostRAMBytes: 8 * gib, DownstreamTracks: 1})
	if err != nil {
		t.Fatalf("lease1 failed: %v", err)
	}
	defer controller.Release(lease1)
	lease2, err := controller.ReserveTier(AdmissionTierDownstream, AdmissionPlan{HostRAMBytes: 8 * gib, DownstreamTracks: 1})
	if err != nil {
		t.Fatalf("lease2 failed: %v", err)
	}
	defer controller.Release(lease2)

	// Large 20 GiB candidate uses the same stable cap (4) and is rejected without shrinking Capacity().DownstreamTracks.
	if err := controller.UpdateCapacity(AdmissionCapacity{
		HostRAMBytes:     budget,
		DownstreamTracks: ResolveDownstreamTracksCapPure(0, 4, budget, stableTrackRAM),
		DiskTempBytes:    64 * gib,
	}); err != nil {
		t.Fatalf("UpdateCapacity failed: %v", err)
	}
	if _, err := controller.ReserveTier(AdmissionTierDownstream, AdmissionPlan{HostRAMBytes: 20 * gib, DownstreamTracks: 1}); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("expected 20 GiB candidate to fail with ErrAdmissionUnavailable, got %v", err)
	}
	if got := controller.Capacity().DownstreamTracks; got != 4 {
		t.Fatalf("expected shared DownstreamTracks cap to remain 4 after rejected candidate, got %d", got)
	}
	if got := controller.ActiveLeasesByTier(AdmissionTierDownstream); got != 2 {
		t.Fatalf("expected 2 active downstream leases preserved, got %d", got)
	}
}

func TestDispatcherRetainedGPUResidencyLifecycle(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	controller, err := NewAdmissionController(AdmissionCapacity{
		DedicatedVRAMBytes: 8 * gib,
		DownstreamTracks:   4,
		CPULanes:           4,
		GPULanes:           1,
		DemucsSlots:        1,
	})
	if err != nil {
		t.Fatalf("NewAdmissionController failed: %v", err)
	}

	d := &Dispatcher{
		config: Config{
			EstimatedDemucsVramGB: 2.0,
		},
		admission: controller,
	}
	client := &WorkerDaemonClient{}

	if err := d.reserveRetainedGPUResidency(client); err != nil {
		t.Fatalf("reserveRetainedGPUResidency failed: %v", err)
	}
	if err := d.reserveRetainedGPUResidency(client); err != nil { // idempotent for same client
		t.Fatalf("idempotent reserveRetainedGPUResidency failed: %v", err)
	}
	if got := controller.ActiveLeasesByTier(AdmissionTierRetainedResidency); got != 1 {
		t.Fatalf("expected 1 retained residency lease, got %d", got)
	}
	if got := controller.UsageByTier(AdmissionTierRetainedResidency).DedicatedVRAMBytes; got != 2*gib {
		t.Fatalf("expected retained VRAM = %d, got %d", 2*gib, got)
	}

	// Second client fails when remaining VRAM capacity is exhausted, surfacing the error.
	if err := controller.UpdateCapacity(AdmissionCapacity{
		DedicatedVRAMBytes: 2 * gib,
		DownstreamTracks:   4,
		CPULanes:           4,
		GPULanes:           1,
		DemucsSlots:        1,
	}); err != nil {
		t.Fatalf("UpdateCapacity failed: %v", err)
	}
	client2 := &WorkerDaemonClient{}
	if err := d.reserveRetainedGPUResidency(client2); !errors.Is(err, ErrAdmissionUnavailable) {
		t.Fatalf("expected ErrAdmissionUnavailable when VRAM ledger is full, got %v", err)
	}

	d.releaseRetainedGPUResidency(client)
	if got := controller.ActiveLeasesByTier(AdmissionTierRetainedResidency); got != 0 {
		t.Fatalf("expected 0 retained residency leases after teardown, got %d", got)
	}
	if got := controller.UsageByTier(AdmissionTierRetainedResidency).DedicatedVRAMBytes; got != 0 {
		t.Fatalf("expected retained VRAM = 0 after teardown, got %d", got)
	}
}

func TestAdmissionQueueSplitAtCap1WithBusyGPU(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	cap1 := ResolveDownstreamTracksCapPure(1, 8, 32*gib, 2*gib)
	if cap1 != 1 {
		t.Fatalf("expected cap=1, got %d", cap1)
	}

	in := GatekeeperInput{
		StorageMode:         StorageModeSHM,
		AvailPhys:           16 * gib,
		EstimatedTaskRam:    4 * gib,
		MinAvailRam:         2 * gib,
		MemoryLoad:          40,
		AvailDisk:           40 * gib,
		MinAvailDisk:        5 * gib,
		GpuUtilization:      99.0,
		AvailVram:           0,
		MinAvailVram:        1 * gib,
		EstimatedTaskVram:   2 * gib,
		GPURequired:         true,
		DedicatedVramKnown:  true,
		DedicatedVramStatus: "busy",
		MaxGpuUtilization:   0.85,
		EnableGpuThrottle:   true,
		RetryDelay:          5 * time.Second,
	}

	// Queue admission at cap=1 succeeds when host RAM & disk are healthy, even while GPU is busy.
	if q := EvaluateQueueAdmissionPure(in); !q.IsGo {
		t.Fatalf("expected EvaluateQueueAdmissionPure GO at cap=1 with healthy host resources, got %s", q.Reason)
	}
	// GPU launch check still rejects the busy GPU.
	if g := EvaluateGPULaunchPure(in); g.IsGo || !g.IsGpuBlock {
		t.Fatalf("expected EvaluateGPULaunchPure NOGO on busy GPU, got IsGo=%v IsGpuBlock=%v", g.IsGo, g.IsGpuBlock)
	}
}
