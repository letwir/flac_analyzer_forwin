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

	"flac_analyzer/orchestrator/planner"
	"flac_analyzer/orchestrator/sysinfo"
)

// AdmissionPlan describes the bounded resources required by one dispatch.
// Byte resources and execution lanes are kept separate so shared GPU memory
// cannot accidentally satisfy a dedicated-VRAM reservation.
type AdmissionPlan struct {
	HostRAMBytes       uint64
	DedicatedVRAMBytes uint64
	DownstreamTracks   int
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

// AdmissionTier separates lifetime accounting for downstream track leases,
// GPU execution grants, and retained GPU daemon residency.
type AdmissionTier uint8

const (
	AdmissionTierDownstream AdmissionTier = iota
	AdmissionTierGPUExecution
	AdmissionTierRetainedResidency
)

func (t AdmissionTier) valid() bool {
	return t <= AdmissionTierRetainedResidency
}

// AdmissionLease is the ownership receipt returned by AtomicReserve / ReserveTier.
// Token identifies the reservation; Release accepts a lease at most once.
type AdmissionLease struct {
	Token              uint64
	Tier               AdmissionTier
	StorageMode        StorageMode
	HostRAMBytes       uint64
	DedicatedVRAMBytes uint64
	DownstreamTracks   int
	CPULanes           int
	GPULanes           int
	DemucsSlots        int
	DiskTempBytes      uint64
	DiskTargetDir      string
	VolumeLease        VolumeLease
}

// AdmissionUsage is a read-only snapshot of currently reserved resources.
type AdmissionUsage = AdmissionPlan

// AdmissionCapacity is the total budget available to the controller.
type AdmissionCapacity = AdmissionPlan

var (
	ErrAdmissionUnavailable  = errors.New("admission resources unavailable")
	ErrInvalidAdmissionPlan  = errors.New("invalid admission plan")
	ErrPermanentBudgetExcess = errors.New("task resource requirements exceed total node capacity")
)

type admissionLeaseState struct {
	tier AdmissionTier
	plan AdmissionPlan
}

// taskAdmissionPlan is the single feeder-side snapshot. StorageMode is kept
// beside the lease because the pipeline must not re-observe memory and choose
// a different backing store after admission.
type taskAdmissionPlan struct {
	request           AdmissionPlan
	storageMode       StorageMode
	totalPhys         uint64
	maxRamRatio       float64
	diskTargetDir     string
	minAvailDiskBytes uint64
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
				if err := d.arenaPool.CloseIdle(); err != nil {
					d.LogWarn("[Admission] idle SHM shrink failed: %v", err)
				}
			}
			d.allocMutex.Unlock()
		}
	} else if decision.Recovered {
		d.LogInfo("[Admission] memory pressure generation %d recovered at %d%%", decision.Generation, load)
	}
	return decision
}

// AdmissionController owns all resource reservations for a dispatch domain.
// It tracks aggregate usage alongside three independent lifetime tiers:
// downstream track leases, GPU execution grants, and retained GPU residency.
type AdmissionController struct {
	mu         sync.Mutex
	capacity   AdmissionCapacity
	used       AdmissionUsage
	tierUsed   map[AdmissionTier]AdmissionUsage
	tierCounts map[AdmissionTier]int
	nextID     uint64
	leases     map[uint64]admissionLeaseState
}

func NewAdmissionController(capacity AdmissionCapacity) (*AdmissionController, error) {
	if err := validateAdmissionPlan(capacity); err != nil {
		return nil, fmt.Errorf("create admission controller: %w", err)
	}
	return &AdmissionController{
		capacity:   capacity,
		tierUsed:   make(map[AdmissionTier]AdmissionUsage),
		tierCounts: make(map[AdmissionTier]int),
		leases:     make(map[uint64]admissionLeaseState),
	}, nil
}

func (c *AdmissionController) UpdateCapacity(capacity AdmissionCapacity) error {
	if c == nil {
		return ErrInvalidAdmissionPlan
	}
	if err := validateAdmissionPlan(capacity); err != nil {
		return err
	}
	c.mu.Lock()
	c.capacity = capacity
	c.mu.Unlock()
	return nil
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

// AtomicReserve rechecks the plan and records a downstream-tier token while
// holding the controller lock. A stale Plan result therefore cannot overcommit.
func (c *AdmissionController) AtomicReserve(plan AdmissionPlan) (AdmissionLease, error) {
	return c.ReserveTier(AdmissionTierDownstream, plan)
}

// ReserveTier atomically reserves resources attributed to an explicit lifetime tier.
func (c *AdmissionController) ReserveTier(tier AdmissionTier, plan AdmissionPlan) (AdmissionLease, error) {
	if c == nil || !tier.valid() {
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", ErrInvalidAdmissionPlan)
	}
	if err := validateAdmissionPlan(plan); err != nil {
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if exceedsFixedAdmissionCapacity(plan, c.capacity) {
		return AdmissionLease{}, fmt.Errorf("reserve admission lease: %w", ErrPermanentBudgetExcess)
	}
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
	if c.tierUsed == nil {
		c.tierUsed = make(map[AdmissionTier]AdmissionUsage)
	}
	if c.tierCounts == nil {
		c.tierCounts = make(map[AdmissionTier]int)
	}
	c.tierUsed[tier] = addAdmissionPlan(c.tierUsed[tier], plan)
	c.tierCounts[tier]++
	c.leases[c.nextID] = admissionLeaseState{tier: tier, plan: plan}
	lease := leaseFromPlan(c.nextID, plan)
	lease.Tier = tier
	return lease, nil
}

func exceedsFixedAdmissionCapacity(request, capacity AdmissionCapacity) bool {
	return request.HostRAMBytes > capacity.HostRAMBytes ||
		request.DownstreamTracks > capacity.DownstreamTracks ||
		request.CPULanes > capacity.CPULanes ||
		request.GPULanes > capacity.GPULanes ||
		request.DemucsSlots > capacity.DemucsSlots
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
	if cur, exists := c.tierUsed[state.tier]; exists {
		c.tierUsed[state.tier] = subtractAdmissionPlan(cur, state.plan)
	}
	if c.tierCounts[state.tier] > 0 {
		c.tierCounts[state.tier]--
	}
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

// Capacity returns a consistent snapshot of the controller's configured capacity.
func (c *AdmissionController) Capacity() AdmissionCapacity {
	if c == nil {
		return AdmissionCapacity{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capacity
}

// UsageByTier returns the currently reserved resources for a single lifetime tier.
func (c *AdmissionController) UsageByTier(tier AdmissionTier) AdmissionUsage {
	if c == nil || !tier.valid() {
		return AdmissionUsage{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tierUsed[tier]
}

// ActiveLeasesByTier returns the number of active leases held in a lifetime tier.
func (c *AdmissionController) ActiveLeasesByTier(tier AdmissionTier) int {
	if c == nil || !tier.valid() {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tierCounts[tier]
}

// ResolveDownstreamTracksCapPure deterministically resolves the maximum concurrent
// downstream track leases from config, worker pool size, and per-track RAM cost.
// PureMorph: (configuredMax, numWorkers, hostRAMBudgetBytes, perTrackRAMBytes) -> cap
func ResolveDownstreamTracksCapPure(configuredMax, numWorkers int, hostRAMBudgetBytes, perTrackRAMBytes uint64) int {
	if configuredMax > 0 {
		return configuredMax
	}
	ramCap := 1
	if perTrackRAMBytes > 0 && hostRAMBudgetBytes >= perTrackRAMBytes {
		computed := hostRAMBudgetBytes / perTrackRAMBytes
		if computed > 4 {
			ramCap = 4
		} else {
			ramCap = int(computed)
		}
	}
	workerCap := max(numWorkers, 1)
	capVal := min(ramCap, min(workerCap, 4))
	if capVal < 1 {
		return 1
	}
	return capVal
}

func cleanupThenRelease(cleanup func() error, release func()) error {
	if release != nil {
		defer release()
	}
	if cleanup == nil {
		return nil
	}
	return cleanup()
}

func validateAdmissionPlan(plan AdmissionPlan) error {
	if plan.DownstreamTracks < 0 || plan.CPULanes < 0 || plan.GPULanes < 0 || plan.DemucsSlots < 0 {
		return ErrInvalidAdmissionPlan
	}
	return nil
}

func fitsAdmissionPlan(request, available AdmissionPlan) bool {
	return request.HostRAMBytes <= available.HostRAMBytes &&
		request.DedicatedVRAMBytes <= available.DedicatedVRAMBytes &&
		request.DownstreamTracks <= available.DownstreamTracks &&
		request.CPULanes <= available.CPULanes &&
		request.GPULanes <= available.GPULanes &&
		request.DemucsSlots <= available.DemucsSlots &&
		request.DiskTempBytes <= available.DiskTempBytes
}

func safeSub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func intSafeSub(a, b int) int {
	if a < b {
		return 0
	}
	return a - b
}

func subtractAdmissionPlan(total, used AdmissionPlan) AdmissionPlan {
	return AdmissionPlan{
		HostRAMBytes:       safeSub(total.HostRAMBytes, used.HostRAMBytes),
		DedicatedVRAMBytes: safeSub(total.DedicatedVRAMBytes, used.DedicatedVRAMBytes),
		DownstreamTracks:   intSafeSub(total.DownstreamTracks, used.DownstreamTracks),
		CPULanes:           intSafeSub(total.CPULanes, used.CPULanes),
		GPULanes:           intSafeSub(total.GPULanes, used.GPULanes),
		DemucsSlots:        intSafeSub(total.DemucsSlots, used.DemucsSlots),
		DiskTempBytes:      safeSub(total.DiskTempBytes, used.DiskTempBytes),
	}
}

func addAdmissionPlan(left, right AdmissionPlan) AdmissionPlan {
	return AdmissionPlan{
		HostRAMBytes:       left.HostRAMBytes + right.HostRAMBytes,
		DedicatedVRAMBytes: left.DedicatedVRAMBytes + right.DedicatedVRAMBytes,
		DownstreamTracks:   left.DownstreamTracks + right.DownstreamTracks,
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
		DownstreamTracks:   plan.DownstreamTracks,
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

	profile := planner.DefaultResourceProfile()
	estimate, _, err := estimateTaskAdmissionResources(task, cfg.NumWorkers, profile)
	if err != nil {
		return taskAdmissionPlan{}, err
	}

	minAvailRAM := gigabytesToBytes(cfg.MinAvailRamGB)
	storageMode, taskRAM, diskBytes := planner.SelectStorageMode(
		estimate,
		memInfo.AvailPhys,
		0,
		minAvailRAM,
		cfg.DiskModeRamThresholdRatio,
		cfg.EnableDiskModeFallback,
	)
	budget := ramBudget(memInfo.TotalPhys, cfg.MaxRamRatio)
	pressure := d.evaluateMemoryPressure(memInfo.MemoryLoad, cfg.EnableDiskModeFallback)
	if pressure.ForceDisk {
		storageMode = StorageModeDisk
		taskRAM, diskBytes = estimate.DiskModeRamBytes, estimate.DiskBytes
	}

	availDisk, diskKnown := availableTaskDisk(cfg, task, d.getVolumeBudget(), VolumeLease{})
	if !diskKnown {
		availDisk = math.MaxUint64
	}

	gpu := sysinfo.GetLatestGpuMetrics()
	if err := validateSharedGPUMemoryObservation(gpu, time.Now()); err != nil {
		return taskAdmissionPlan{}, err
	}
	estimatedVRAM := gigabytesToBytes(cfg.EstimatedDemucsVramGB)
	if estimatedVRAM == 0 {
		estimatedVRAM = 1024 * 1024 * 1024
	}
	availVRAM := uint64(0)
	dedicatedKnown := false
	dedicatedStatus := "metrics unavailable"
	utilization := 0.0
	if gpu != nil {
		availVRAM = gpu.AvailableVramBytes
		dedicatedKnown = gpu.IsDedicatedAvailable() && gpu.DedicatedCapacityValid && gpu.DedicatedTotalBytes > 0
		dedicatedStatus = gpu.StatusDetail
		utilization = gpu.UtilizationPercent
	}
	sharedGPUReserve := max(estimate.WorkingVramBytes, estimatedVRAM)
	taskRAM, ok := checkedAddUint64(taskRAM, sharedGPUReserve)
	if !ok {
		return taskAdmissionPlan{}, fmt.Errorf("%w: host RAM plus shared-GPU safety reserve overflow", ErrPermanentBudgetExcess)
	}
	storageMode, taskRAM, diskBytes, err = applyPromotionAndValidateDiskPure(
		storageMode,
		estimate,
		sharedGPUReserve,
		taskRAM,
		diskBytes,
		budget,
		availDisk,
		gigabytesToBytes(cfg.MinAvailDiskGB),
		diskKnown,
		cfg.EnableDiskModeFallback,
	)
	if err != nil {
		return taskAdmissionPlan{}, err
	}

	stableTrackRAM := max(gigabytesToBytes(cfg.EstimatedWorkerRamGB), 1024*1024*1024)
	downstreamCap := ResolveDownstreamTracksCapPure(cfg.MaxDownstreamTracks, cfg.NumWorkers, budget, stableTrackRAM)
	retryDelay := secondsToDuration(cfg.GatekeeperRetryDelaySec)
	gkIn := GatekeeperInput{
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
		DedicatedVramStatus: dedicatedStatus,
		MaxGpuUtilization:   cfg.MaxGpuUtilizationRatio,
		EnableGpuThrottle:   cfg.EnableGpuThrottle,
		AllowHighMemoryDisk: pressure.ForceDisk,
		RetryDelay:          retryDelay,
	}
	decision := EvaluateQueueAdmissionPure(gkIn)
	if !decision.IsGo {
		return taskAdmissionPlan{}, fmt.Errorf("gatekeeper NOGO: %s", decision.Reason)
	}

	d.admissionMu.Lock()
	defer d.admissionMu.Unlock()
	var dedicatedTotal uint64
	if dedicatedKnown && gpu != nil {
		dedicatedTotal = gpu.DedicatedTotalBytes
	} else if d.admission != nil {
		dedicatedTotal = d.admission.Capacity().DedicatedVRAMBytes
	}
	capacity := AdmissionCapacity{
		HostRAMBytes:       ramBudget(memInfo.TotalPhys, cfg.MaxRamRatio),
		DedicatedVRAMBytes: dedicatedTotal,
		DownstreamTracks:   downstreamCap,
		CPULanes:           max(cfg.NumWorkers, 1),
		GPULanes:           1,
		DemucsSlots:        1,
		DiskTempBytes:      availDisk,
	}
	if d.admission == nil {
		controller, createErr := NewAdmissionController(capacity)
		if createErr != nil {
			return taskAdmissionPlan{}, createErr
		}
		d.admission = controller
	} else if err := d.admission.UpdateCapacity(capacity); err != nil {
		return taskAdmissionPlan{}, fmt.Errorf("refresh admission capacity: %w", err)
	}

	return taskAdmissionPlan{
		request: AdmissionPlan{
			HostRAMBytes:     taskRAM,
			DiskTempBytes:    diskBytes,
			DownstreamTracks: 1,
		},
		storageMode:       storageMode,
		totalPhys:         memInfo.TotalPhys,
		maxRamRatio:       cfg.MaxRamRatio,
		diskTargetDir:     cfgDiskTempTargetDir(cfg),
		minAvailDiskBytes: gigabytesToBytes(cfg.MinAvailDiskGB),
	}, nil
}

func evaluateExecutionGPULaunchPure(cfg Config, gpu *sysinfo.GpuMetrics, now time.Time) error {
	if gpu == nil || gpu.IsStale(now, sysinfo.DefaultGpuStaleThreshold) {
		return fmt.Errorf("GPU launch telemetry unavailable")
	}
	estimatedVRAM := gigabytesToBytes(cfg.EstimatedDemucsVramGB)
	if estimatedVRAM == 0 {
		estimatedVRAM = 1024 * 1024 * 1024
	}
	dec := EvaluateGPULaunchPure(GatekeeperInput{
		StorageMode:         StorageModeSHM,
		AvailPhys:           math.MaxUint64,
		AvailDisk:           math.MaxUint64,
		GpuUtilization:      gpu.UtilizationPercent,
		AvailVram:           gpu.AvailableVramBytes,
		MinAvailVram:        gigabytesToBytes(cfg.MinAvailVramGB),
		EstimatedTaskVram:   estimatedVRAM,
		GPURequired:         true,
		DedicatedVramKnown:  gpu.IsDedicatedAvailable() && gpu.DedicatedCapacityValid && gpu.DedicatedTotalBytes > 0,
		DedicatedVramStatus: gpu.StatusDetail,
		MaxGpuUtilization:   cfg.MaxGpuUtilizationRatio,
		EnableGpuThrottle:   cfg.EnableGpuThrottle,
		RetryDelay:          secondsToDuration(cfg.GatekeeperRetryDelaySec),
	})
	if !dec.IsGo {
		return fmt.Errorf("GPU launch NOGO: %s", dec.Reason)
	}
	return nil
}

func (d *Dispatcher) reserveExecutionAdmission() (AdmissionLease, error) {
	return d.reserveExecutionAdmissionWithGPU(sysinfo.GetLatestGpuMetrics(), time.Now())
}

func (d *Dispatcher) reserveExecutionAdmissionWithGPU(gpu *sysinfo.GpuMetrics, now time.Time) (AdmissionLease, error) {
	cfg := d.GetConfig()
	if err := evaluateExecutionGPULaunchPure(cfg, gpu, now); err != nil {
		return AdmissionLease{}, err
	}
	vram := gigabytesToBytes(cfg.EstimatedDemucsVramGB)
	if vram == 0 {
		vram = 1024 * 1024 * 1024
	}
	d.admissionMu.Lock()
	controller := d.admission
	if controller != nil && controller.Capacity().DedicatedVRAMBytes == 0 && gpu != nil && gpu.DedicatedCapacityValid && gpu.DedicatedTotalBytes > 0 {
		capCopy := controller.Capacity()
		capCopy.DedicatedVRAMBytes = gpu.DedicatedTotalBytes
		_ = controller.UpdateCapacity(capCopy)
	}
	d.admissionMu.Unlock()
	if controller == nil {
		return AdmissionLease{}, ErrAdmissionUnavailable
	}
	return controller.ReserveTier(AdmissionTierGPUExecution, AdmissionPlan{
		DedicatedVRAMBytes: vram,
		CPULanes:           1,
		GPULanes:           1,
		DemucsSlots:        1,
	})
}

func (d *Dispatcher) reserveRetainedGPUResidency(client *WorkerDaemonClient) error {
	if d == nil || client == nil {
		return nil
	}
	d.admissionMu.Lock()
	controller := d.admission
	if controller == nil {
		d.admissionMu.Unlock()
		return nil
	}
	if d.retainedGPULeases != nil {
		if existing, ok := d.retainedGPULeases[client]; ok && existing.Token != 0 {
			d.admissionMu.Unlock()
			return nil
		}
	}
	d.admissionMu.Unlock()

	vram := max(gigabytesToBytes(d.GetConfig().EstimatedDemucsVramGB), 1024*1024*1024)
	lease, err := controller.ReserveTier(AdmissionTierRetainedResidency, AdmissionPlan{
		DedicatedVRAMBytes: vram,
	})
	if err != nil {
		return err
	}

	d.admissionMu.Lock()
	if d.admission == controller {
		if d.retainedGPULeases == nil {
			d.retainedGPULeases = make(map[*WorkerDaemonClient]AdmissionLease)
		}
		if existing, ok := d.retainedGPULeases[client]; !ok || existing.Token == 0 {
			d.retainedGPULeases[client] = lease
			d.admissionMu.Unlock()
			return nil
		}
	}
	d.admissionMu.Unlock()
	controller.Release(lease)
	return nil
}

func (d *Dispatcher) releaseRetainedGPUResidency(client *WorkerDaemonClient) {
	if d == nil || client == nil {
		return
	}
	d.admissionMu.Lock()
	controller := d.admission
	var lease AdmissionLease
	var ok bool
	if d.retainedGPULeases != nil {
		lease, ok = d.retainedGPULeases[client]
		if ok {
			delete(d.retainedGPULeases, client)
		}
	}
	d.admissionMu.Unlock()
	if controller != nil && ok && lease.Token != 0 {
		controller.Release(lease)
	}
}

func (d *Dispatcher) releaseAllRetainedGPUResidency() {
	if d == nil {
		return
	}
	d.admissionMu.Lock()
	controller := d.admission
	if len(d.retainedGPULeases) == 0 {
		d.admissionMu.Unlock()
		return
	}
	leases := make([]AdmissionLease, 0, len(d.retainedGPULeases))
	for c, lease := range d.retainedGPULeases {
		if lease.Token != 0 {
			leases = append(leases, lease)
		}
		delete(d.retainedGPULeases, c)
	}
	d.admissionMu.Unlock()
	if controller != nil {
		for _, lease := range leases {
			controller.Release(lease)
		}
	}
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
	budgetLedger := d.getVolumeBudget()
	lease, err := controller.ReserveTier(AdmissionTierDownstream, planned.request)
	if err != nil {
		return AdmissionLease{}, err
	}
	lease.StorageMode = planned.storageMode
	lease.DiskTempBytes = planned.request.DiskTempBytes
	lease.DiskTargetDir = planned.diskTargetDir
	if planned.request.DiskTempBytes > 0 {
		volLease, volErr := budgetLedger.Reserve(planned.diskTargetDir, planned.request.DiskTempBytes, planned.minAvailDiskBytes)
		if volErr != nil {
			controller.Release(lease)
			return AdmissionLease{}, volErr
		}
		lease.VolumeLease = volLease
	}
	ramLease, ok := d.reserveRamAdmission(task, ramAdmission{
		storageMode: planned.storageMode,
		ramBytes:    planned.request.HostRAMBytes,
	}, planned.totalPhys, planned.maxRamRatio)
	if !ok {
		if lease.VolumeLease.Token != 0 {
			budgetLedger.Release(lease.VolumeLease)
		}
		controller.Release(lease)
		return AdmissionLease{}, ErrAdmissionUnavailable
	}

	d.admissionMu.Lock()
	if d.taskLeases == nil {
		d.taskLeases = make(map[string]AdmissionLease)
	}
	key := admissionKey(task)
	_, exists := d.taskLeases[key]
	if !exists {
		d.taskLeases[key] = lease
	}
	d.admissionMu.Unlock()
	if exists {
		d.releaseRamAdmission(task, ramLease)
		if lease.VolumeLease.Token != 0 {
			budgetLedger.Release(lease.VolumeLease)
		}
		controller.Release(lease)
		return AdmissionLease{}, fmt.Errorf("task already has an admission lease")
	}
	return lease, nil
}

func (d *Dispatcher) tryReserveTaskAdmission(task TaskPayload) (AdmissionLease, error) {
	if d.reserveTaskFn != nil {
		return d.reserveTaskFn(task)
	}
	return d.reserveTaskAdmission(task)
}

func (d *Dispatcher) recheckTaskAdmissionBeforeDispatch(task TaskPayload, lease AdmissionLease) error {
	cfg := d.GetConfig()
	memInfo, err := sysinfo.GetMemoryInfo()
	if err != nil || memInfo == nil || memInfo.TotalPhys == 0 {
		return fmt.Errorf("last-mile memory observation unavailable")
	}
	gpu := sysinfo.GetLatestGpuMetrics()
	if err := validateSharedGPUMemoryObservation(gpu, time.Now()); err != nil {
		return err
	}
	if lease.VolumeLease.Token != 0 && lease.VolumeLease.VolumeKey != "" {
		curKey, errCur := VolumeKeyE(cfgDiskTempTargetDir(cfg))
		if errCur != nil || curKey != lease.VolumeLease.VolumeKey {
			return fmt.Errorf("last-mile admission NOGO: disk cache target volume changed or became unresolvable after reservation")
		}
	}
	availDisk, diskKnown := availableTaskDisk(cfg, task, d.getVolumeBudget(), lease.VolumeLease)
	if (lease.StorageMode == StorageModeDisk || lease.DiskTempBytes > 0) && lease.DiskTargetDir != "" {
		if _, _, _, errTarget := d.getVolumeBudget().probePath(lease.DiskTargetDir, lease.VolumeLease); errTarget != nil {
			return fmt.Errorf("last-mile disk target validation failed: %w", errTarget)
		}
	}
	if err := validateDiskModeCapacityPure(lease.StorageMode, diskKnown, availDisk, lease.DiskTempBytes, gigabytesToBytes(cfg.MinAvailDiskGB)); err != nil {
		return fmt.Errorf("last-mile disk validation failed: %w", err)
	}
	if !diskKnown {
		availDisk = math.MaxUint64
	}
	pressure := d.evaluateMemoryPressure(memInfo.MemoryLoad, cfg.EnableDiskModeFallback)
	gkIn := buildLastMileGatekeeperInputPure(cfg, memInfo, gpu, availDisk, diskKnown, pressure, lease)
	decision := EvaluateQueueAdmissionPure(gkIn)
	if !decision.IsGo {
		return fmt.Errorf("last-mile admission NOGO: %s", decision.Reason)
	}
	// GlobalMemoryStatusEx.AvailPhys already reflects current shared-GPU memory use.
	// Do not subtract SharedUsedBytes or active reservations from it a second time.
	return nil
}

func buildLastMileGatekeeperInputPure(
	cfg Config,
	memInfo *sysinfo.MemoryInfo,
	gpu *sysinfo.GpuMetrics,
	availDisk uint64,
	diskKnown bool,
	pressure MemoryPressureDecision,
	lease AdmissionLease,
) GatekeeperInput {
	return GatekeeperInput{
		StorageMode:         lease.StorageMode,
		EstimatedTaskDisk:   lease.DiskTempBytes,
		AvailPhys:           memInfo.AvailPhys,
		EstimatedTaskRam:    lease.HostRAMBytes,
		MinAvailRam:         gigabytesToBytes(cfg.MinAvailRamGB),
		MemoryLoad:          memInfo.MemoryLoad,
		AvailDisk:           availDisk,
		MinAvailDisk:        gigabytesToBytes(cfg.MinAvailDiskGB),
		GpuUtilization:      gpu.UtilizationPercent,
		AvailVram:           gpu.AvailableVramBytes,
		MinAvailVram:        gigabytesToBytes(cfg.MinAvailVramGB),
		EstimatedTaskVram:   max(gigabytesToBytes(cfg.EstimatedDemucsVramGB), 1024*1024*1024),
		GPURequired:         true,
		DedicatedVramKnown:  gpu.IsDedicatedAvailable() && gpu.DedicatedCapacityValid && gpu.DedicatedTotalBytes > 0,
		DedicatedVramStatus: gpu.StatusDetail,
		MaxGpuUtilization:   cfg.MaxGpuUtilizationRatio,
		EnableGpuThrottle:   cfg.EnableGpuThrottle,
		AllowHighMemoryDisk: pressure.ForceDisk,
		RetryDelay:          secondsToDuration(cfg.GatekeeperRetryDelaySec),
	}
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
	if lease.VolumeLease.Token != 0 {
		d.getVolumeBudget().Release(lease.VolumeLease)
	}
}

func cfgDiskTempTargetDir(cfg Config) string {
	if cfg.DiskCacheDir != "" {
		return cfg.DiskCacheDir
	}
	return os.TempDir()
}

func (d *Dispatcher) getVolumeBudget() *VolumeBudgetLedger {
	if d == nil {
		return NewVolumeBudgetLedger(nil)
	}
	d.admissionMu.Lock()
	defer d.admissionMu.Unlock()
	if d.volumeBudget == nil {
		d.volumeBudget = NewVolumeBudgetLedger(nil)
	}
	return d.volumeBudget
}

func availableTaskDisk(cfg Config, task TaskPayload, ledger *VolumeBudgetLedger, exclude VolumeLease) (uint64, bool) {
	paths := []string{cfg.QueueDir, os.TempDir(), cfg.DiskCacheDir, cfg.SpectroCacheDir}
	if task.FlacPath != "" {
		paths = append(paths, filepath.Dir(task.FlacPath))
	}
	var min uint64 = math.MaxUint64
	known := false
	for _, path := range paths {
		if path == "" {
			continue
		}
		var (
			effectiveFree uint64
			probeErr      error
		)
		if ledger != nil {
			var reserved uint64
			var rawAvail uint64
			_, rawAvail, reserved, probeErr = ledger.probePath(path, exclude)
			if probeErr == nil {
				effectiveFree = safeSub(rawAvail, reserved)
			}
		} else {
			anchor, ancErr := resolveAnchorPath(path)
			if ancErr != nil {
				continue
			}
			info, err := sysinfo.GetDiskFreeSpace(anchor)
			if err != nil || info == nil {
				continue
			}
			effectiveFree = info.FreeBytesAvailable
		}
		if probeErr != nil {
			continue
		}
		known = true
		if effectiveFree < min {
			min = effectiveFree
		}
	}
	return min, known
}

func gigabytesToBytes(gb float64) uint64 {
	if gb <= 0 || math.IsNaN(gb) || math.IsInf(gb, 0) {
		return 0
	}
	bytes := gb * 1024 * 1024 * 1024
	if math.IsInf(bytes, 0) || bytes >= float64(math.MaxUint64) {
		return math.MaxUint64
	}
	return uint64(bytes)
}

func ramBudget(total uint64, ratio float64) uint64 {
	if total == 0 || ratio <= 0 || ratio > 1 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0
	}
	budget := float64(total) * ratio
	if math.IsInf(budget, 0) || budget >= float64(math.MaxUint64) {
		return math.MaxUint64
	}
	return uint64(budget)
}

func secondsToDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 20 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func checkedAddUint64(a, b uint64) (uint64, bool) {
	if b > math.MaxUint64-a {
		return 0, false
	}
	return a + b, true
}

func estimateTaskAdmissionResources(task TaskPayload, numWorkers int, profile planner.ResourceProfile) (planner.ResourceEstimate, int, error) {
	plan, err := BuildAnalysisExecutionPlan(task.AnalysisDecision)
	if err != nil {
		return planner.ResourceEstimate{}, 0, fmt.Errorf("invalid admission analysis plan: %w", err)
	}
	if plan.Decision == Skip {
		return planner.ResourceEstimate{}, 0, fmt.Errorf("skip decisions do not require admission")
	}
	stemCount := uint64(7)
	switch plan.Decision {
	case MixOnly:
		stemCount = 1
	case StemsOnly:
		stemCount = 6
	}
	profile.StemCount = stemCount
	cpuConsumers := GetCPUConsumerCount(task, max(numWorkers, 1), int(stemCount))
	if cpuConsumers < 1 {
		return planner.ResourceEstimate{}, 0, fmt.Errorf("%w: invalid CPU consumer estimate", ErrPermanentBudgetExcess)
	}
	profile.CPUParallelism = uint64(cpuConsumers)
	estimate := planner.EstimateTaskResources(toPlannerTask(task), profile)
	if estimate.Overflow {
		return planner.ResourceEstimate{}, 0, fmt.Errorf("%w: resource estimate overflow", ErrPermanentBudgetExcess)
	}
	return estimate, cpuConsumers, nil
}

func validateSharedGPUMemoryObservation(gpu *sysinfo.GpuMetrics, now time.Time) error {
	if gpu == nil || gpu.IsStale(now, sysinfo.DefaultGpuStaleThreshold) || !gpu.SharedUsageValid {
		return fmt.Errorf("GPU shared-memory observation unavailable")
	}
	return nil
}

// applyPromotionAndValidateDiskPure promotes to StorageModeDisk under RAM budget pressure,
// enforces the permanent RAM budget ceiling first (so transient disk checks never mask ErrPermanentBudgetExcess),
// and then re-validates disk observation & free-space floor against DiskBytes.
func applyPromotionAndValidateDiskPure(
	storageMode StorageMode,
	estimate planner.ResourceEstimate,
	sharedGPUReserve uint64,
	taskRAM uint64,
	diskBytes uint64,
	budget uint64,
	availDisk uint64,
	minAvailDisk uint64,
	diskKnown bool,
	enableDiskFallback bool,
) (StorageMode, uint64, uint64, error) {
	if taskRAM > budget && enableDiskFallback && storageMode != StorageModeDisk {
		storageMode = StorageModeDisk
		var ok bool
		taskRAM, ok = checkedAddUint64(estimate.DiskModeRamBytes, sharedGPUReserve)
		if !ok {
			return storageMode, taskRAM, diskBytes, fmt.Errorf("%w: disk-mode RAM plus shared-GPU safety reserve overflow", ErrPermanentBudgetExcess)
		}
		diskBytes = estimate.DiskBytes
	}
	if taskRAM > budget {
		return storageMode, taskRAM, diskBytes, fmt.Errorf("%w: required RAM %d exceeds configured total-RAM budget %d (storage=%s; shared-GPU reserve=%d)", ErrPermanentBudgetExcess, taskRAM, budget, storageMode, sharedGPUReserve)
	}
	if err := validateDiskModeCapacityPure(storageMode, diskKnown, availDisk, diskBytes, minAvailDisk); err != nil {
		return storageMode, taskRAM, diskBytes, err
	}
	return storageMode, taskRAM, diskBytes, nil
}
func validateDiskModeCapacityPure(storageMode StorageMode, diskKnown bool, availDisk, diskBytes, minAvailDisk uint64) error {
	if storageMode != StorageModeDisk && diskBytes == 0 {
		return nil
	}
	if !diskKnown {
		return fmt.Errorf("disk observation unavailable for disk mode")
	}
	requiredDisk, ok := checkedAddUint64(minAvailDisk, diskBytes)
	if !ok {
		return fmt.Errorf("%w: required disk estimate overflow", ErrPermanentBudgetExcess)
	}
	if availDisk < requiredDisk {
		return fmt.Errorf("insufficient disk space for disk mode: avail=%d required=%d", availDisk, requiredDisk)
	}
	return nil
}
