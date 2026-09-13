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

	"flac_analyzer/orchestrator/state"
)

func testAdmissionCapacity() AdmissionCapacity {
	return AdmissionCapacity{
		HostRAMBytes:       8,
		DedicatedVRAMBytes: 6,
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
	if lease.Token == 0 || lease.HostRAMBytes != request.HostRAMBytes || lease.DedicatedVRAMBytes != request.DedicatedVRAMBytes || lease.CPULanes != request.CPULanes || lease.GPULanes != request.GPULanes || lease.DemucsSlots != request.DemucsSlots || lease.DiskTempBytes != request.DiskTempBytes {
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
	invalid := AdmissionPlan{CPULanes: -1}
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
	blocked := AdmissionPlan{HostRAMBytes: 5, DedicatedVRAMBytes: 2, CPULanes: 1, GPULanes: 1, DemucsSlots: 1}
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
	if _, err := controller.AtomicReserve(blocked); !errors.Is(err, ErrAdmissionUnavailable) {
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
	completed := make(chan TaskPayload, 4)
	d := &Dispatcher{
		config:         Config{GatekeeperRetryDelaySec: 60},
		db:             db,
		taskQueue:      make(chan TaskPayload, 8),
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
	d.fillTaskQueue()
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
