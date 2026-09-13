package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"flac_analyzer/orchestrator/sysinfo"
)

// AdmissionPlan describes the bounded resources required by one dispatch.
// Byte resources and execution lanes are kept separate so shared GPU memory
// cannot accidentally satisfy a dedicated-VRAM reservation.
type AdmissionPlan struct {
	HostRAMBytes       uint64
	DedicatedVRAMBytes uint64
	CPULanes           int
	GPULanes           int
	DemucsSlots        int
	DiskTempBytes      uint64
}

// AdmissionDecision is the side-effect-free result of Plan. AtomicReserve
// rechecks the request under the controller lock because this snapshot may
// become stale before the caller reserves it.
type AdmissionDecision struct {
	Request    AdmissionPlan
	Available  AdmissionPlan
	Admissible bool
	Reason     string
}

// AdmissionLease is the ownership receipt returned by AtomicReserve. Token
// identifies the reservation; Release accepts a lease at most once.
type AdmissionLease struct {
	Token              uint64
	StorageMode        StorageMode
	HostRAMBytes       uint64
	DedicatedVRAMBytes uint64
	CPULanes           int
	GPULanes           int
	DemucsSlots        int
	DiskTempBytes      uint64
}

// AdmissionUsage is a read-only snapshot of currently reserved resources.
type AdmissionUsage = AdmissionPlan

// AdmissionCapacity is the total budget available to the controller.
type AdmissionCapacity = AdmissionPlan

var (
	ErrAdmissionUnavailable = errors.New("admission resources unavailable")
	ErrInvalidAdmissionPlan = errors.New("invalid admission plan")
)

type admissionLeaseState struct {
	plan AdmissionPlan
}

// taskAdmissionPlan is the single feeder-side snapshot. StorageMode is kept
// beside the lease because the pipeline must not re-observe memory and choose
// a different backing store after admission.
type taskAdmissionPlan struct {
	request     AdmissionPlan
	storageMode StorageMode
	totalPhys   uint64
	maxRamRatio float64
}

type MemoryPressureState struct {
	Active        bool
	Generation    uint64
	EnterPercent  uint32
	ResumePercent uint32
}

type MemoryPressureDecision struct {
	Active     bool
	Entered    bool
	Recovered  bool
	Generation uint64
	ForceDisk  bool
}

func EvaluateMemoryPressurePure(state MemoryPressureState, load uint32, diskFallback bool) (MemoryPressureState, MemoryPressureDecision) {
	if state.EnterPercent == 0 {
		state.EnterPercent = 90
	}
	if state.ResumePercent == 0 || state.ResumePercent >= state.EnterPercent {
		state.ResumePercent = 82
	}
	decision := MemoryPressureDecision{Active: state.Active, Generation: state.Generation}
	if !state.Active && load >= state.EnterPercent {
		state.Active = true
		state.Generation++
		decision.Entered = true
	}
	if state.Active && load <= state.ResumePercent {
		state.Active = false
		decision.Recovered = true
	}
	decision.Active = state.Active
	decision.Generation = state.Generation
	decision.ForceDisk = state.Active && diskFallback
	return state, decision
}

func (d *Dispatcher) evaluateMemoryPressure(load uint32, diskFallback bool) MemoryPressureDecision {
	d.pressureMu.Lock()
	state, decision := EvaluateMemoryPressurePure(d.memoryPressure, load, diskFallback)
	d.memoryPressure = state
	d.pressureMu.Unlock()
	if decision.Entered {
		d.LogWarn("[Admission] memory pressure generation %d entered at %d%%; shrinking idle SHM cache and preferring DiskMmap", decision.Generation, load)
		if d.arenaPool != nil {
			d.allocMutex.Lock()
			if atomic.LoadInt32(&d.activeTaskCount) == 0 {
				d.arenaPool.Close()
			}
			d.allocMutex.Unlock()
		}
	} else if decision.Recovered {
		d.LogInfo("[Admission] memory pressure generation %d recovered at %d%%", decision.Generation, load)
	}
	return decision
}

// AdmissionController owns all resource reservations for a dispatch domain.
// It intentionally has no worker, queue, or OS-resource knowledge; those are
// supplied by the later integration phase.
type AdmissionController struct {
	mu       sync.Mutex
	capacity AdmissionCapacity
	used     AdmissionUsage
	nextID   uint64
	leases   map[uint64]admissionLeaseState
}

func NewAdmissionController(capacity AdmissionCapacity) (*AdmissionController, error) {
	if err := validateAdmissionPlan(capacity); err != nil {
		return nil, fmt.Errorf("create admission controller: %w", err)
	}
	return &AdmissionController{
		capacity: capacity,
		leases:   make(map[uint64]admissionLeaseState),
	}, nil
}

// Plan takes a consistent snapshot without changing reservations.
func (c *AdmissionController) Plan(request AdmissionPlan) AdmissionDecision {
	if c == nil {
		return AdmissionDecision{Request: request, Reason: "nil admission controller"}
	}
	if err := validateAdmissionPlan(request); err != nil {
		return AdmissionDecision{Request: request, Reason: err.Error()}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	available := subtractAdmissionPlan(c.capacity, c.used)
	if fitsAdmissionPlan(request, available) {
		return AdmissionDecision{Request: request, Available: available, Admissible: true, Reason: "admissible"}
	}
	return AdmissionDecision{Request: request, Available: available, Reason: "one or more resource budgets are exhausted"}
}

// AtomicReserve rechecks the plan and records one token while holding the
// controller lock. A stale Plan result therefore cannot overcommit capacity.
func (c *AdmissionController) AtomicReserve(plan AdmissionPlan) (AdmissionLease, error) {
	if c == nil {
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", ErrInvalidAdmissionPlan)
	}
	if err := validateAdmissionPlan(plan); err != nil {
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	available := subtractAdmissionPlan(c.capacity, c.used)
	if !fitsAdmissionPlan(plan, available) {
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", ErrAdmissionUnavailable)
	}
	c.nextID++
	if c.nextID == 0 {
		// Token wrap would make an old lease potentially alias a new one.
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", ErrAdmissionUnavailable)
	}
	c.used = addAdmissionPlan(c.used, plan)
	c.leases[c.nextID] = admissionLeaseState{plan: plan}
	return leaseFromPlan(c.nextID, plan), nil
}

// Dispatch executes work while holding a lease and always releases it. The
// release defer also runs when the callback returns an error or panics.
func (c *AdmissionController) Dispatch(ctx context.Context, lease AdmissionLease, fn func(context.Context, AdmissionLease) error) (err error) {
	if c == nil || ctx == nil || lease.Token == 0 || fn == nil {
		return ErrInvalidAdmissionPlan
	}
	defer func() {
		c.Release(lease)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(ctx, lease)
}

// Admit composes Plan, AtomicReserve, Dispatch, and Release for one task.
// It does not wait for resources; a caller can requeue when admission is not
// currently possible, leaving workers free for admissible tasks.
func (c *AdmissionController) Admit(ctx context.Context, request AdmissionPlan, fn func(context.Context, AdmissionLease) error) error {
	decision := c.Plan(request)
	if !decision.Admissible {
		if decision.Reason == "" {
			return ErrAdmissionUnavailable
		}
		return fmt.Errorf("plan admission: %w: %s", ErrAdmissionUnavailable, decision.Reason)
	}
	lease, err := c.AtomicReserve(request)
	if err != nil {
		return err
	}
	return c.Dispatch(ctx, lease, fn)
}

// Release returns a reservation to the shared budget. Releasing an unknown,
// stale, or already released token is a no-op and returns false.
func (c *AdmissionController) Release(lease AdmissionLease) bool {
	if c == nil || lease.Token == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.leases[lease.Token]
	if !ok {
		return false
	}
	c.used = subtractAdmissionPlan(c.used, state.plan)
	delete(c.leases, lease.Token)
	return true
}

func (c *AdmissionController) Usage() AdmissionUsage {
	if c == nil {
		return AdmissionUsage{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

func validateAdmissionPlan(plan AdmissionPlan) error {
	if plan.CPULanes < 0 || plan.GPULanes < 0 || plan.DemucsSlots < 0 {
		return ErrInvalidAdmissionPlan
	}
	return nil
}

func fitsAdmissionPlan(request, available AdmissionPlan) bool {
	return request.HostRAMBytes <= available.HostRAMBytes &&
		request.DedicatedVRAMBytes <= available.DedicatedVRAMBytes &&
		request.CPULanes <= available.CPULanes &&
		request.GPULanes <= available.GPULanes &&
		request.DemucsSlots <= available.DemucsSlots &&
		request.DiskTempBytes <= available.DiskTempBytes
}

func subtractAdmissionPlan(total, used AdmissionPlan) AdmissionPlan {
	return AdmissionPlan{
		HostRAMBytes:       total.HostRAMBytes - used.HostRAMBytes,
		DedicatedVRAMBytes: total.DedicatedVRAMBytes - used.DedicatedVRAMBytes,
		CPULanes:           total.CPULanes - used.CPULanes,
		GPULanes:           total.GPULanes - used.GPULanes,
		DemucsSlots:        total.DemucsSlots - used.DemucsSlots,
		DiskTempBytes:      total.DiskTempBytes - used.DiskTempBytes,
	}
}

func addAdmissionPlan(left, right AdmissionPlan) AdmissionPlan {
	return AdmissionPlan{
		HostRAMBytes:       left.HostRAMBytes + right.HostRAMBytes,
		DedicatedVRAMBytes: left.DedicatedVRAMBytes + right.DedicatedVRAMBytes,
		CPULanes:           left.CPULanes + right.CPULanes,
		GPULanes:           left.GPULanes + right.GPULanes,
		DemucsSlots:        left.DemucsSlots + right.DemucsSlots,
		DiskTempBytes:      left.DiskTempBytes + right.DiskTempBytes,
	}
}

func leaseFromPlan(token uint64, plan AdmissionPlan) AdmissionLease {
	return AdmissionLease{
		Token:              token,
		HostRAMBytes:       plan.HostRAMBytes,
		DedicatedVRAMBytes: plan.DedicatedVRAMBytes,
		CPULanes:           plan.CPULanes,
		GPULanes:           plan.GPULanes,
		DemucsSlots:        plan.DemucsSlots,
		DiskTempBytes:      plan.DiskTempBytes,
	}
}

func (d *Dispatcher) admissionPlanForTask(task TaskPayload) (taskAdmissionPlan, error) {
	cfg := d.GetConfig()
	memInfo, err := sysinfo.GetMemoryInfo()
	if err != nil || memInfo == nil || memInfo.TotalPhys == 0 {
		return taskAdmissionPlan{}, fmt.Errorf("memory observation unavailable")
	}

	minAvailRAM := gigabytesToBytes(cfg.MinAvailRamGB)
	storageMode, taskRAM, diskBytes := DetermineStorageModePure(
		task,
		memInfo.AvailPhys,
		0,
		minAvailRAM,
		cfg.DiskModeRamThresholdRatio,
		cfg.EnableDiskModeFallback,
	)
	pressure := d.evaluateMemoryPressure(memInfo.MemoryLoad, cfg.EnableDiskModeFallback)
	if pressure.ForceDisk {
		storageMode = StorageModeDisk
		_, taskRAM, diskBytes = DetermineStorageModePure(task, 0, 0, minAvailRAM, 1, true)
	}

	availDisk, diskKnown := availableTaskDisk(cfg, task)
	if (storageMode == StorageModeDisk || minAvailRAM == 0 && diskBytes > 0) && !diskKnown {
		return taskAdmissionPlan{}, fmt.Errorf("disk observation unavailable")
	}
	if !diskKnown {
		availDisk = math.MaxUint64
	}

	gpu := sysinfo.GetLatestGpuMetrics()
	estimatedVRAM := gigabytesToBytes(cfg.EstimatedDemucsVramGB)
	if estimatedVRAM == 0 {
		estimatedVRAM = 1024 * 1024 * 1024
	}
	availVRAM := uint64(0)
	dedicatedKnown := false
	utilization := 0.0
	if gpu != nil {
		availVRAM = gpu.AvailableVramBytes
		dedicatedKnown = gpu.IsDedicatedAvailable()
		utilization = gpu.UtilizationPercent
	}

	retryDelay := secondsToDuration(cfg.GatekeeperRetryDelaySec)
	decision := EvaluateGoNoGoPure(GatekeeperInput{
		StorageMode:         storageMode,
		EstimatedTaskDisk:   diskBytes,
		AvailPhys:           memInfo.AvailPhys,
		EstimatedTaskRam:    taskRAM,
		MinAvailRam:         minAvailRAM,
		MemoryLoad:          memInfo.MemoryLoad,
		AvailDisk:           availDisk,
		MinAvailDisk:        gigabytesToBytes(cfg.MinAvailDiskGB),
		GpuUtilization:      utilization,
		AvailVram:           availVRAM,
		MinAvailVram:        gigabytesToBytes(cfg.MinAvailVramGB),
		EstimatedTaskVram:   estimatedVRAM,
		GPURequired:         true,
		DedicatedVramKnown:  dedicatedKnown,
		MaxGpuUtilization:   cfg.MaxGpuUtilizationRatio,
		EnableGpuThrottle:   cfg.EnableGpuThrottle,
		AllowHighMemoryDisk: pressure.ForceDisk,
		RetryDelay:          retryDelay,
	})
	if !decision.IsGo {
		return taskAdmissionPlan{}, fmt.Errorf("gatekeeper NOGO: %s", decision.Reason)
	}

	d.admissionMu.Lock()
	defer d.admissionMu.Unlock()
	if d.admission == nil {
		capacity := AdmissionCapacity{
			HostRAMBytes:       ramBudget(memInfo.TotalPhys, cfg.MaxRamRatio),
			DedicatedVRAMBytes: availVRAM,
			CPULanes:           max(cfg.NumWorkers, 1),
			// The adaptive scheduler starts at one and may contract back to one.
			// Admission must never promise more execution slots than that floor.
			GPULanes:      1,
			DemucsSlots:   1,
			DiskTempBytes: availDisk,
		}
		controller, createErr := NewAdmissionController(capacity)
		if createErr != nil {
			return taskAdmissionPlan{}, createErr
		}
		d.admission = controller
	}

	return taskAdmissionPlan{
		request: AdmissionPlan{
			HostRAMBytes:  taskRAM,
			DiskTempBytes: diskBytes,
		},
		storageMode: storageMode,
		totalPhys:   memInfo.TotalPhys,
		maxRamRatio: cfg.MaxRamRatio,
	}, nil
}

func (d *Dispatcher) reserveExecutionAdmission() (AdmissionLease, error) {
	cfg := d.GetConfig()
	vram := gigabytesToBytes(cfg.EstimatedDemucsVramGB)
	if vram == 0 {
		vram = 1024 * 1024 * 1024
	}
	d.admissionMu.Lock()
	controller := d.admission
	d.admissionMu.Unlock()
	if controller == nil {
		return AdmissionLease{}, ErrAdmissionUnavailable
	}
	return controller.AtomicReserve(AdmissionPlan{
		DedicatedVRAMBytes: vram,
		CPULanes:           1,
		GPULanes:           1,
		DemucsSlots:        1,
	})
}

func (d *Dispatcher) releaseExecutionAdmission(lease AdmissionLease) {
	d.admissionMu.Lock()
	controller := d.admission
	d.admissionMu.Unlock()
	if controller != nil {
		controller.Release(lease)
	}
}

func (d *Dispatcher) reserveTaskAdmission(task TaskPayload) (AdmissionLease, error) {
	planned, err := d.admissionPlanForTask(task)
	if err != nil {
		return AdmissionLease{}, err
	}
	d.admissionMu.Lock()
	controller := d.admission
	d.admissionMu.Unlock()
	if controller == nil {
		return AdmissionLease{}, fmt.Errorf("admission controller unavailable")
	}
	lease, err := controller.AtomicReserve(planned.request)
	if err != nil {
		return AdmissionLease{}, err
	}
	lease.StorageMode = planned.storageMode
	ramLease, ok := d.reserveRamAdmission(task, ramAdmission{
		storageMode: planned.storageMode,
		ramBytes:    planned.request.HostRAMBytes,
	}, planned.totalPhys, planned.maxRamRatio)
	if !ok {
		controller.Release(lease)
		return AdmissionLease{}, ErrAdmissionUnavailable
	}

	d.admissionMu.Lock()
	defer d.admissionMu.Unlock()
	if d.taskLeases == nil {
		d.taskLeases = make(map[string]AdmissionLease)
	}
	key := admissionKey(task)
	if _, exists := d.taskLeases[key]; exists {
		d.releaseRamAdmission(task, ramLease)
		controller.Release(lease)
		return AdmissionLease{}, fmt.Errorf("task already has an admission lease")
	}
	d.taskLeases[key] = lease
	return lease, nil
}

func (d *Dispatcher) tryReserveTaskAdmission(task TaskPayload) (AdmissionLease, error) {
	if d.reserveTaskFn != nil {
		return d.reserveTaskFn(task)
	}
	return d.reserveTaskAdmission(task)
}

func (d *Dispatcher) takeTaskAdmission(task TaskPayload) (AdmissionLease, bool) {
	d.admissionMu.Lock()
	defer d.admissionMu.Unlock()
	lease, ok := d.taskLeases[admissionKey(task)]
	if ok {
		delete(d.taskLeases, admissionKey(task))
	}
	return lease, ok
}

func (d *Dispatcher) releaseTaskAdmission(task TaskPayload, lease AdmissionLease) {
	d.admissionMu.Lock()
	controller := d.admission
	if stored, ok := d.taskLeases[admissionKey(task)]; ok && stored.Token == lease.Token {
		delete(d.taskLeases, admissionKey(task))
	}
	d.admissionMu.Unlock()
	if controller != nil {
		controller.Release(lease)
	}
}

func availableTaskDisk(cfg Config, task TaskPayload) (uint64, bool) {
	paths := []string{cfg.QueueDir, os.TempDir()}
	if task.FlacPath != "" {
		paths = append(paths, filepath.Dir(task.FlacPath))
	}
	var min uint64 = math.MaxUint64
	known := false
	for _, path := range paths {
		if path == "" {
			continue
		}
		info, err := sysinfo.GetDiskFreeSpace(path)
		if err != nil || info == nil {
			continue
		}
		known = true
		if info.FreeBytesAvailable < min {
			min = info.FreeBytesAvailable
		}
	}
	return min, known
}

func gigabytesToBytes(gb float64) uint64 {
	if gb <= 0 || math.IsNaN(gb) || math.IsInf(gb, 0) {
		return 0
	}
	return uint64(gb * 1024 * 1024 * 1024)
}

func ramBudget(total uint64, ratio float64) uint64 {
	if total == 0 || ratio <= 0 || ratio > 1 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0
	}
	return uint64(float64(total) * ratio)
}

func secondsToDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 20 * time.Second
	}
	return time.Duration(seconds) * time.Second
}
