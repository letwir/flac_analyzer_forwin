package dispatcher

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestVolumeBudgetSameVolumeSharedAccounting(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	tmpBase := t.TempDir()
	diskCacheDir := filepath.Join(tmpBase, "disk_cache")
	spectroCacheDir := filepath.Join(tmpBase, "spectro_cache")

	if VolumeKey(diskCacheDir) != VolumeKey(spectroCacheDir) {
		t.Fatalf("expected same volume key, got %q vs %q", VolumeKey(diskCacheDir), VolumeKey(spectroCacheDir))
	}

	avail := 10 * gib
	minFree := 2 * gib
	ledger := NewVolumeBudgetLedger(func(string) (uint64, error) {
		return avail, nil
	})

	leaseDisk, err := ledger.Reserve(diskCacheDir, 4*gib, minFree)
	if err != nil {
		t.Fatalf("Reserve(diskCacheDir) failed: %v", err)
	}
	leaseSpectro, err := ledger.Reserve(spectroCacheDir, 3*gib, minFree)
	if err != nil {
		t.Fatalf("Reserve(spectroCacheDir) failed: %v", err)
	}
	if got := ledger.ReservedBytes(diskCacheDir); got != 7*gib {
		t.Fatalf("shared ReservedBytes = %d, want %d", got, 7*gib)
	}

	// 7 GiB reserved + 2 GiB minFree = 9 GiB used against 10 GiB avail -> 2 GiB request must fail.
	if _, err := ledger.Reserve(spectroCacheDir, 2*gib, minFree); !errors.Is(err, ErrVolumeBudgetExhausted) {
		t.Fatalf("expected ErrVolumeBudgetExhausted on overcommit, got %v", err)
	}

	// Releasing disk lease frees 4 GiB for subsequent spectro reservation.
	if !ledger.Release(leaseDisk) {
		t.Fatal("expected leaseDisk Release to succeed")
	}
	leaseSpectro2, err := ledger.Reserve(spectroCacheDir, 2*gib, minFree)
	if err != nil {
		t.Fatalf("Reserve after release failed: %v", err)
	}
	if !ledger.Release(leaseSpectro) || !ledger.Release(leaseSpectro2) {
		t.Fatal("expected remaining releases to succeed")
	}
	if got := ledger.ReservedBytes(spectroCacheDir); got != 0 {
		t.Fatalf("expected 0 reserved bytes after full release, got %d", got)
	}
}

func TestVolumeBudgetIdempotentReleaseAndCleanupOrder(t *testing.T) {
	const mib = uint64(1024 * 1024)
	dir := t.TempDir()
	ledger := NewVolumeBudgetLedger(func(string) (uint64, error) {
		return 100 * mib, nil
	})

	lease, err := ledger.Reserve(dir, 40*mib, 10*mib)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}

	// nil cleanup callback must return error and preserve the reservation.
	if err := ledger.ReleaseAfterCleanup(lease, nil); err == nil {
		t.Fatal("expected error for nil cleanup callback")
	}
	if got := ledger.ReservedBytes(dir); got != 40*mib {
		t.Fatalf("expected 40 MiB preserved after nil cleanup, got %d", got)
	}

	var reservedDuringCleanup uint64
	cleanupErr := ledger.ReleaseAfterCleanup(lease, func() error {
		reservedDuringCleanup = ledger.ReservedBytes(dir)
		return nil
	})
	if cleanupErr != nil {
		t.Fatalf("ReleaseAfterCleanup failed: %v", cleanupErr)
	}
	if reservedDuringCleanup != 40*mib {
		t.Fatalf("expected 40 MiB still reserved during cleanup, got %d", reservedDuringCleanup)
	}
	if got := ledger.ReservedBytes(dir); got != 0 {
		t.Fatalf("expected 0 reserved after ReleaseAfterCleanup, got %d", got)
	}
	if ledger.Release(lease) {
		t.Fatal("expected second Release of same lease to return false")
	}

	// Failed cleanup MUST preserve reservation so caller can retry cleanup and then release.
	lease2, err := ledger.Reserve(dir, 25*mib, 10*mib)
	if err != nil {
		t.Fatalf("Reserve 2 failed: %v", err)
	}
	sentinel := errors.New("remove failed")
	if err := ledger.ReleaseAfterCleanup(lease2, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("expected cleanup sentinel error, got %v", err)
	}
	if got := ledger.ReservedBytes(dir); got != 25*mib {
		t.Fatalf("expected 25 MiB preserved after failed cleanup, got %d", got)
	}
	if err := ledger.ReleaseAfterCleanup(lease2, func() error { return nil }); err != nil {
		t.Fatalf("expected retry cleanup to succeed, got %v", err)
	}
	if got := ledger.ReservedBytes(dir); got != 0 {
		t.Fatalf("expected 0 reserved after successful retry cleanup, got %d", got)
	}
}

func TestVolumeKeyAliasAndDistinctVolumes(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real_target")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatalf("mkdir target failed: %v", err)
	}
	nonExistentLeaf := filepath.Join(target, "nested", "req-1")
	alias := filepath.Join(base, "alias_junction")

	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", "mklink", "/J", alias, target)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("mklink /J failed: %v (%s)", err, string(out))
		}
	} else {
		if err := os.Symlink(target, alias); err != nil {
			t.Fatalf("symlink failed: %v", err)
		}
	}

	kTarget := VolumeKey(target)
	kLeaf := VolumeKey(nonExistentLeaf)
	kAlias := VolumeKey(alias)
	if kTarget == "" || kTarget != kLeaf || kTarget != kAlias {
		t.Fatalf("expected identical VolumeKey across target/non-existent leaf/alias, got target=%q leaf=%q alias=%q", kTarget, kLeaf, kAlias)
	}
	if runtime.GOOS == "windows" {
		// Must resolve to canonical \\?\VOLUME{GUID}\ rather than plain drive-letter prefix.
		if len(kTarget) < 12 || kTarget[:11] != `\\?\VOLUME{` {
			t.Fatalf("expected canonical Windows volume GUID, got %q", kTarget)
		}
	}

	// Verify literal '..' traversal after symlink resolves target before '..' collapse.
	parentViaLink := alias + string(filepath.Separator) + ".." + string(filepath.Separator) + "real_target" + string(filepath.Separator) + "cache"
	resolvedAnchor, ancErr := resolveAnchorPath(parentViaLink)
	if ancErr != nil {
		t.Fatalf("resolveAnchorPath(%q) failed: %v", parentViaLink, ancErr)
	}
	if kTraversal := VolumeKey(parentViaLink); kTraversal != kTarget || resolvedAnchor == "" {
		t.Fatalf("expected symlink + '..' traversal VolumeKey %q (anchor=%q), got %q", kTarget, resolvedAnchor, kTraversal)
	}

	// Verify native identity failure propagates instead of falling back to drive/path strings.
	failingLedger := NewVolumeBudgetLedger(func(string) (uint64, error) {
		return 10 << 30, nil
	})
	failingLedger.resolveKeyFn = func(string) (string, error) {
		return "", errors.New("canonical volume lookup failed")
	}
	if _, err := failingLedger.Reserve(target, 1<<20, 0); err == nil {
		t.Fatal("expected Reserve to fail when canonical volume identity lookup fails")
	}

	// Verify distinct VolumeKeys keep independent byte accounting in VolumeBudgetLedger.
	const gib = uint64(1024 * 1024 * 1024)
	ledger := NewVolumeBudgetLedger(func(string) (uint64, error) {
		return 10 * gib, nil
	})
	leaseA, err := ledger.Reserve(alias, 6*gib, 2*gib)
	if err != nil {
		t.Fatalf("Reserve(alias) failed: %v", err)
	}
	defer ledger.Release(leaseA)

	// Verify second reservation on the same aliased volume sees the 6 GiB reservation made via alias.
	if _, err := ledger.Reserve(nonExistentLeaf, 3*gib, 2*gib); !errors.Is(err, ErrVolumeBudgetExhausted) {
		t.Fatalf("expected same-volume reservation via non-existent leaf to hit ErrVolumeBudgetExhausted, got %v", err)
	}
}

func TestAdmissionDuplicateDiskReservationRollbackNoDeadlock(t *testing.T) {
	const gib = uint64(1024 * 1024 * 1024)
	dir := t.TempDir()
	ledger := NewVolumeBudgetLedger(func(string) (uint64, error) {
		return 20 * gib, nil
	})
	controller, err := NewAdmissionController(AdmissionCapacity{
		HostRAMBytes:     32 * gib,
		DownstreamTracks: 4,
		DiskTempBytes:    20 * gib,
	})
	if err != nil {
		t.Fatalf("NewAdmissionController failed: %v", err)
	}

	d := &Dispatcher{
		config: Config{
			NumWorkers:          4,
			MaxRamRatio:         0.625,
			MinAvailDiskGB:      1.0,
			DiskCacheDir:        dir,
			EnableGpuThrottle:   true,
			MaxGpuUtilizationRatio: 0.85,
		},
		admission:     controller,
		volumeBudget:  ledger,
		taskLeases:    make(map[string]AdmissionLease),
		ramAdmissions: make(map[string]ramAdmission),
	}

	task := TaskPayload{FlacPath: filepath.Join(dir, "dup.flac"), TrackNumber: 1}
	existingVolLease, err := ledger.Reserve(dir, 2*gib, 1*gib)
	if err != nil {
		t.Fatalf("initial ledger reserve failed: %v", err)
	}
	existingCtrlLease, err := controller.ReserveTier(AdmissionTierDownstream, AdmissionPlan{
		HostRAMBytes:     2 * gib,
		DiskTempBytes:    2 * gib,
		DownstreamTracks: 1,
	})
	if err != nil {
		t.Fatalf("initial controller reserve failed: %v", err)
	}
	d.taskLeases[admissionKey(task)] = existingCtrlLease

	// Simulate the duplicate-task rollback path in reserveTaskAdmission (must not deadlock on admissionMu).
	done := make(chan struct{})
	go func() {
		defer close(done)
		budgetLedger := d.getVolumeBudget()
		dupCtrlLease, rErr := controller.ReserveTier(AdmissionTierDownstream, AdmissionPlan{
			HostRAMBytes:     2 * gib,
			DiskTempBytes:    2 * gib,
			DownstreamTracks: 1,
		})
		if rErr != nil {
			t.Errorf("dup ReserveTier failed: %v", rErr)
			return
		}
		dupVolLease, vErr := budgetLedger.Reserve(dir, 2*gib, 1*gib)
		if vErr != nil {
			controller.Release(dupCtrlLease)
			t.Errorf("dup volume reserve failed: %v", vErr)
			return
		}
		d.admissionMu.Lock()
		_, exists := d.taskLeases[admissionKey(task)]
		d.admissionMu.Unlock()
		if exists {
			budgetLedger.Release(dupVolLease)
			controller.Release(dupCtrlLease)
		}
	}()
	<-done

	if got := ledger.ReservedBytes(dir); got != 2*gib {
		t.Fatalf("expected only existing 2 GiB volume reservation preserved, got %d", got)
	}
	if got := controller.Usage().DownstreamTracks; got != 1 {
		t.Fatalf("expected only existing 1 downstream track preserved, got %d", got)
	}
	ledger.Release(existingVolLease)
	controller.Release(existingCtrlLease)
}

func TestAvailableTaskDiskSubtractsVolumeReservations(t *testing.T) {
	dir := t.TempDir()
	rawAvail, diskErr := defaultVolumeFreeSpace(dir)
	if diskErr != nil || rawAvail < 1024*1024 {
		t.Skipf("disk space unavailable for %s: %v", dir, diskErr)
	}

	ledger := NewVolumeBudgetLedger(func(string) (uint64, error) {
		return rawAvail, nil
	})
	reserveBytes := rawAvail / 2
	lease, err := ledger.Reserve(dir, reserveBytes, 0)
	if err != nil {
		t.Fatalf("Reserve failed: %v", err)
	}
	defer ledger.Release(lease)

	cfg := Config{QueueDir: dir, DiskCacheDir: dir}
	effective, known := availableTaskDisk(cfg, TaskPayload{}, ledger, VolumeLease{})
	if !known {
		t.Fatal("expected disk availability to be known")
	}
	if effective > rawAvail-reserveBytes+64*1024*1024 {
		t.Fatalf("expected availableTaskDisk to subtract %d reserved bytes from raw ~%d, got %d", reserveBytes, rawAvail, effective)
	}

	// Excluding the task's own lease restores its own reserved bytes on recheck.
	excluded, knownEx := availableTaskDisk(cfg, TaskPayload{}, ledger, lease)
	if !knownEx {
		t.Fatal("expected disk availability to be known when excluding own lease")
	}
	if excluded <= effective {
		t.Fatalf("expected excluded free space (%d) > non-excluded (%d)", excluded, effective)
	}
}

func TestVolumeBudgetConcurrentStress(t *testing.T) {
	const mib = uint64(1024 * 1024)
	dir := t.TempDir()
	avail := 500 * mib
	minFree := 100 * mib // max reservable = 400 MiB

	var overcommitted atomic.Bool
	ledger := NewVolumeBudgetLedger(func(string) (uint64, error) {
		return avail, nil
	})

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				lease, err := ledger.Reserve(dir, 50*mib, minFree)
				if err != nil {
					if !errors.Is(err, ErrVolumeBudgetExhausted) {
						t.Errorf("unexpected reserve error: %v", err)
					}
					continue
				}
				if ledger.ReservedBytes(dir) > avail-minFree {
					overcommitted.Store(true)
				}
				ledger.Release(lease)
			}
		}()
	}
	wg.Wait()

	if overcommitted.Load() {
		t.Fatal("volume budget overcommitted beyond avail - minFree under concurrency")
	}
	if got := ledger.ReservedBytes(dir); got != 0 {
		t.Fatalf("expected 0 reserved bytes after concurrent drain, got %d", got)
	}
}
