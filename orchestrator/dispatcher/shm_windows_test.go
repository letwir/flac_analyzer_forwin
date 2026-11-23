package dispatcher

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedMemory(t *testing.T) {
	name := "Local\\TestSHM123"
	size := uint32(1024)

	shm, err := NewSharedMemory(name, size)
	if shm != nil {
		t.Cleanup(func() { _ = shm.Close() })
	}
	if err != nil {
		t.Fatalf("Failed to create shared memory: %v", err)
	}

	t.Logf("SharedMemory created successfully. isLocked: %v", shm.isLocked)

	testData := []byte("hello shared memory")
	if err := shm.Write(testData); err != nil {
		t.Fatalf("Failed to write to shared memory: %v", err)
	}

	if !bytes.Equal(shm.data[:len(testData)], testData) {
		t.Fatalf("Data mismatch. Got: %s", string(shm.data[:len(testData)]))
	}

	if err := shm.Freeze(); err != nil {
		t.Fatalf("Failed to freeze shared memory: %v", err)
	}

	if err := shm.Unfreeze(); err != nil {
		t.Fatalf("Failed to unfreeze shared memory: %v", err)
	}

	newData := []byte("reused shared memory")
	if err := shm.Write(newData); err != nil {
		t.Fatalf("Failed to write after unfreeze: %v", err)
	}

	if !bytes.Equal(shm.data[:len(newData)], newData) {
		t.Fatalf("Data mismatch after unfreeze. Got: %s", string(shm.data[:len(newData)]))
	}
}

func TestEnsureCapacity(t *testing.T) {
	name := "Local\\TestSHMCapacity"
	initialSize := uint32(512)

	shm, err := NewSharedMemory(name, initialSize)
	if shm != nil {
		t.Cleanup(func() { _ = shm.Close() })
	}
	if err != nil {
		t.Fatalf("Failed to create shared memory: %v", err)
	}

	// Same or smaller capacity -> no-op reuse
	if err := shm.EnsureCapacity(256); err != nil {
		t.Fatalf("EnsureCapacity smaller failed: %v", err)
	}
	if shm.Size != initialSize {
		t.Fatalf("Expected size %d, got %d", initialSize, shm.Size)
	}

	// Larger capacity -> expansion
	expandedSize := uint32(2048)
	if err := shm.EnsureCapacity(expandedSize); err != nil {
		t.Fatalf("EnsureCapacity larger failed: %v", err)
	}
	if shm.Size != expandedSize {
		t.Fatalf("Expected size %d, got %d", expandedSize, shm.Size)
	}

	largeData := bytes.Repeat([]byte("A"), 1500)
	if err := shm.Write(largeData); err != nil {
		t.Fatalf("Failed to write large data after expansion: %v", err)
	}
	if !bytes.Equal(shm.data[:len(largeData)], largeData) {
		t.Fatalf("Data mismatch after expansion")
	}
}

func TestWorkingSetExpansion(t *testing.T) {
	minSize, maxSize, flags, err := GetProcessWorkingSetSize()
	if err != nil {
		t.Logf("GetProcessWorkingSetSize note: %v", err)
	} else {
		t.Logf("Current WorkingSet - Min: %d KB, Max: %d KB, Flags: 0x%x", minSize/1024, maxSize/1024, flags)
	}

	// Expand working set for a 32MB buffer
	if err := ExpandWorkingSetForSize(32 * 1024 * 1024); err != nil {
		t.Logf("ExpandWorkingSetForSize note: %v", err)
	}

	newMin, newMax, _, err := GetProcessWorkingSetSize()
	if err == nil {
		t.Logf("Expanded WorkingSet - Min: %d KB, Max: %d KB", newMin/1024, newMax/1024)
	}
}

func TestVirtualLock(t *testing.T) {
	// Enable working set expansion for test process
	_ = EnableProcessWorkingSetLock(256, 4096)

	name := "Local\\TestSHM_VirtualLock"
	size := uint32(8 * 1024 * 1024) // 8MB

	shm, err := NewSharedMemoryWithLock(name, size, true)
	if shm != nil {
		t.Cleanup(func() { _ = shm.Close() })
	}
	if err != nil {
		t.Fatalf("Failed to create shared memory: %v", err)
	}

	if !shm.isLocked {
		t.Errorf("Expected shm.isLocked to be true for 8MB SHM, got false")
	} else {
		t.Logf("Successfully pinned 8MB SHM to physical RAM (isLocked: true)")
	}

	// Test writing to pinned memory
	data := bytes.Repeat([]byte("LOCKED_DATA_"), 1000)
	if err := shm.Write(data); err != nil {
		t.Fatalf("Failed to write to locked SHM: %v", err)
	}

	// Test EnsureCapacity with lock retention
	expandedSize := uint32(16 * 1024 * 1024) // 16MB
	if err := shm.EnsureCapacity(expandedSize); err != nil {
		t.Fatalf("EnsureCapacity failed: %v", err)
	}
	if !shm.isLocked {
		t.Errorf("Expected shm.isLocked to be true after expansion to 16MB, got false")
	} else {
		t.Logf("Successfully maintained physical RAM lock after expansion to 16MB (isLocked: true)")
	}
}

func TestShmArenaPool(t *testing.T) {
	_ = EnableProcessWorkingSetLock(256, 4096)
	pool := NewShmArenaPool(true)
	t.Cleanup(func() { _ = pool.Close() })

	lease1, err := pool.AcquireArenaLease(1)
	if err != nil || lease1 == nil || lease1.Set == nil {
		t.Fatalf("Expected worker1 arena lease, got (%v, %v)", lease1, err)
	}
	t.Cleanup(func() { _ = lease1.CloseAndRelease() })
	worker1 := lease1.Set

	stems := []string{"mix", "bass", "drums", "vocals", "other", "guitar", "piano"}
	for _, stem := range stems {
		shm, err := worker1.GetOrCreateArena(stem, 1024*1024*2) // 2MB each
		if err != nil {
			t.Fatalf("Failed to get or create arena for stem %s: %v", stem, err)
		}
		if shm.Size != 1024*1024*2 {
			t.Fatalf("Expected size 2MB for stem %s, got %d", stem, shm.Size)
		}
		if !shm.isLocked {
			t.Logf("Note: stem %s isLocked: %v", stem, shm.isLocked)
		}
		if err := shm.Write([]byte("stem:" + stem)); err != nil {
			t.Fatalf("Failed to write to stem %s: %v", stem, err)
		}
	}

	tags := worker1.GetTagsMap()
	if len(tags) != len(stems) {
		t.Fatalf("Expected %d tags, got %d", len(stems), len(tags))
	}

	if err := worker1.FreezeAll(); err != nil {
		t.Fatalf("FreezeAll failed: %v", err)
	}

	if err := worker1.UnfreezeAll(); err != nil {
		t.Fatalf("UnfreezeAll failed: %v", err)
	}

	// Re-write to test reuse
	for _, stem := range stems {
		shm, err := worker1.GetOrCreateArena(stem, 1024*1024*2)
		if err != nil {
			t.Fatalf("Failed to reuse arena for stem %s: %v", stem, err)
		}
		if err := shm.Write([]byte("reused:" + stem)); err != nil {
			t.Fatalf("Failed to write reused data for stem %s: %v", stem, err)
		}
	}

	// Test expansion in arena set
	shmMix, err := worker1.GetOrCreateArena("mix", 1024*1024*4) // 4MB
	if err != nil {
		t.Fatalf("Failed to expand mix arena: %v", err)
	}
	if shmMix.Size != 1024*1024*4 {
		t.Fatalf("Expected mix size 4MB, got %d", shmMix.Size)
	}

	if err := lease1.CloseAndRelease(); err != nil {
		t.Fatalf("lease1.CloseAndRelease() failed: %v", err)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("pool.Close() failed: %v", err)
	}
	if len(worker1.arenas) != 0 {
		t.Fatalf("Expected 0 arenas after pool close, got %d", len(worker1.arenas))
	}
}

func TestShmPythonInterop(t *testing.T) {
	name := "Local\\TestPythonInterop_mix"
	size := uint32(1024 * 1024 * 4) // 4MB

	shm, err := NewSharedMemory(name, size)
	if shm != nil {
		t.Cleanup(func() { _ = shm.Close() })
	}
	if err != nil {
		t.Fatalf("Failed to create shared memory: %v", err)
	}

	// 1. Python writes to SHM
	pyWriteScript := `
import mmap, sys
import numpy as np
import shm_interop

data = np.full((2, 44100), 0.42, dtype=np.float32)
shm = shm_interop.write_to_shm("Local\\TestPythonInterop_mix", data)
print("WRITE_OK")
`
	parentDir := findProjectRoot()
	venvPython := filepath.Join(parentDir, ".venv", "Scripts", "python.exe")
	if _, err := os.Stat(venvPython); err != nil {
		venvPython = "python.exe"
	}

	cmdWrite := exec.Command(venvPython, "-c", pyWriteScript)
	cmdWrite.Dir = parentDir
	outWrite, err := cmdWrite.CombinedOutput()
	if err != nil {
		t.Fatalf("Python write failed: %v, output: %s", err, string(outWrite))
	}
	if !strings.Contains(string(outWrite), "WRITE_OK") {
		t.Fatalf("Unexpected python write output: %s", string(outWrite))
	}

	// 2. Go Freezes SHM
	if err := shm.Freeze(); err != nil {
		t.Fatalf("Failed to freeze SHM: %v", err)
	}

	// 3. Python reads from Read-Only SHM
	pyReadScript := `
import numpy as np
import shm_interop

shm, arr = shm_interop.attach_shm_read_only("Local\\TestPythonInterop_mix", (2, 44100), "float32")
assert np.allclose(arr, 0.42), f"Values mismatch: {arr[0, :5]}"
shm.close()
print("READ_OK")
`
	cmdRead := exec.Command(venvPython, "-c", pyReadScript)
	cmdRead.Dir = parentDir
	outRead, err := cmdRead.CombinedOutput()
	if err != nil {
		t.Fatalf("Python read failed: %v, output: %s", err, string(outRead))
	}
	if !strings.Contains(string(outRead), "READ_OK") {
		t.Fatalf("Unexpected python read output: %s", string(outRead))
	}

	// 4. Go Unfreezes SHM for Reuse
	if err := shm.Unfreeze(); err != nil {
		t.Fatalf("Failed to unfreeze SHM: %v", err)
	}

	// 5. Python writes second round to same SHM
	pyWrite2Script := `
import numpy as np
import shm_interop

data = np.full((2, 44100), 0.99, dtype=np.float32)
shm = shm_interop.write_to_shm("Local\\TestPythonInterop_mix", data)
print("WRITE2_OK")
`
	cmdWrite2 := exec.Command(venvPython, "-c", pyWrite2Script)
	cmdWrite2.Dir = parentDir
	outWrite2, err := cmdWrite2.CombinedOutput()
	if err != nil {
		t.Fatalf("Python write round 2 failed: %v, output: %s", err, string(outWrite2))
	}
	if !strings.Contains(string(outWrite2), "WRITE2_OK") {
		t.Fatalf("Unexpected python write round 2 output: %s", string(outWrite2))
	}

	// 6. Freeze & Read round 2
	if err := shm.Freeze(); err != nil {
		t.Fatalf("Failed to freeze SHM round 2: %v", err)
	}

	pyRead2Script := `
import numpy as np
import shm_interop

shm, arr = shm_interop.attach_shm_read_only("Local\\TestPythonInterop_mix", (2, 44100), "float32")
assert np.allclose(arr, 0.99), f"Round 2 values mismatch: {arr[0, :5]}"
shm.close()
print("READ2_OK")
`
	cmdRead2 := exec.Command(venvPython, "-c", pyRead2Script)
	cmdRead2.Dir = parentDir
	outRead2, err := cmdRead2.CombinedOutput()
	if err != nil {
		t.Fatalf("Python read round 2 failed: %v, output: %s", err, string(outRead2))
	}
	if !strings.Contains(string(outRead2), "READ2_OK") {
		t.Fatalf("Unexpected python read round 2 output: %s", string(outRead2))
	}
}

func TestComputeAdaptiveTimeoutPure(t *testing.T) {
	// Case 1: Short track (1 minute: 44100 * 60 samples)
	shortTask := TaskPayload{
		StartSample: 0,
		EndSample:   44100 * 60,
	}
	shortDur := ComputeAdaptiveTimeoutPure(shortTask, 300, 1.5, 7200)
	expectedShort := 390 * 1000000000 // 390s (time.Duration nanoseconds)
	if shortDur.Seconds() != 390 {
		t.Errorf("Expected 390s for 1-minute track, got %v", shortDur)
	}
	_ = expectedShort

	highRateTask := TaskPayload{
		StartSample: 0,
		EndSample:   96000 * 60,
		SampleRate:  96000,
	}
	highRateDur := ComputeAdaptiveTimeoutPure(highRateTask, 300, 1.5, 7200)
	if highRateDur.Seconds() != 390 {
		t.Errorf("Expected 390s for a 1-minute 96kHz track, got %v", highRateDur)
	}

	// Case 2: Standard 5-minute track (44100 * 300 samples)
	stdTask := TaskPayload{
		StartSample: 0,
		EndSample:   44100 * 300,
	}
	stdDur := ComputeAdaptiveTimeoutPure(stdTask, 300, 1.5, 7200)
	if stdDur.Seconds() != 750 { // 300 + 300 * 1.5 = 750s (12.5 min)
		t.Errorf("Expected 750s for 5-minute track, got %v", stdDur)
	}

	// Case 3: 55-minute talk/radio track (44100 * 3300 samples, like Perfume Track 3)
	longTask := TaskPayload{
		StartSample: 24678948,
		EndSample:   24678948 + (44100 * 3300),
	}
	longDur := ComputeAdaptiveTimeoutPure(longTask, 300, 1.5, 7200)
	if longDur.Seconds() != 5250 { // 300 + 3300 * 1.5 = 5250s (87.5 min)
		t.Errorf("Expected 5250s for 55-minute track, got %v", longDur)
	}

	// Case 4: Extreme 100-minute track -> Clamped to maxTimeout (7200s)
	extremeTask := TaskPayload{
		StartSample: 0,
		EndSample:   44100 * 6000,
	}
	extremeDur := ComputeAdaptiveTimeoutPure(extremeTask, 300, 1.5, 7200)
	if extremeDur.Seconds() != 7200 { // 300 + 6000 * 1.5 = 9300 -> clamped to 7200
		t.Errorf("Expected 7200s max clamp, got %v", extremeDur)
	}

	// Case 5: Zero/negative fallback defense
	zeroDur := ComputeAdaptiveTimeoutPure(stdTask, 0, 0, 0)
	if zeroDur.Seconds() != 750 {
		t.Errorf("Expected fallback to 750s, got %v", zeroDur)
	}

	// Case 6: Single file without explicit sample range (FileSize fallback)
	fileTask := TaskPayload{
		StartSample: 0,
		EndSample:   0,
		FileSize:    50 * 1024 * 1024, // 50MB ≈ 297s, clamped to 600s min
	}
	fileDur := ComputeAdaptiveTimeoutPure(fileTask, 300, 1.5, 7200)
	if fileDur.Seconds() != 1200 { // 300 + 600 * 1.5 = 1200s (20 min)
		t.Errorf("Expected 1200s for single FLAC file fallback, got %v", fileDur)
	}
}

func TestShmArenaPoolConcurrentStress(t *testing.T) {
	pool := NewShmArenaPool(false)

	// Duplicate lease for the same worker ID must fail until released, and a stale
	// lease handle must NOT release or close a subsequent owner's lease.
	lease1, err := pool.AcquireArenaLease(42)
	if err != nil || lease1 == nil {
		t.Fatalf("first AcquireArenaLease failed: %v", err)
	}
	if _, err := lease1.Set.GetOrCreateArena("mix", 4096); err != nil {
		t.Fatalf("lease1 GetOrCreateArena failed: %v", err)
	}
	if second, err := pool.AcquireArenaLease(42); err == nil || second != nil {
		t.Fatalf("expected duplicate AcquireArenaLease to fail while in use")
	}
	if err := lease1.CloseAndRelease(); err != nil {
		t.Fatalf("lease1 CloseAndRelease failed: %v", err)
	}

	lease2, err := pool.AcquireArenaLease(42)
	if err != nil || lease2 == nil {
		t.Fatalf("expected reacquisition after release, got (%v, %v)", lease2, err)
	}
	if _, err := lease2.Set.GetOrCreateArena("mix", 4096); err != nil {
		t.Fatalf("lease2 GetOrCreateArena failed: %v", err)
	}
	// Stale CloseAndRelease on lease1 must be a safe no-op and leave lease2 intact.
	if err := lease1.CloseAndRelease(); err != nil {
		t.Fatalf("stale CloseAndRelease returned unexpected error: %v", err)
	}
	if err := lease2.Set.VerifyIntegrity([]string{"mix"}); err != nil {
		t.Fatalf("stale lease corrupted active lease2 arenas: %v", err)
	}
	// CloseIdle must skip active lease2.
	if err := pool.CloseIdle(); err != nil {
		t.Fatalf("CloseIdle failed: %v", err)
	}
	if err := lease2.Set.VerifyIntegrity([]string{"mix"}); err != nil {
		t.Fatalf("CloseIdle closed active lease2 arenas: %v", err)
	}

	// Same-set concurrent FreezeStem / UnfreezeAll / Close serialization.
	var sameSetWG sync.WaitGroup
	for g := 0; g < 8; g++ {
		sameSetWG.Add(1)
		go func() {
			defer sameSetWG.Done()
			for k := 0; k < 25; k++ {
				if err := lease2.Set.FreezeStem("mix"); err != nil {
					t.Errorf("FreezeStem failed: %v", err)
				}
				if err := lease2.Set.FreezeAll(); err != nil {
					t.Errorf("FreezeAll failed: %v", err)
				}
				if err := lease2.Set.UnfreezeAll(); err != nil {
					t.Errorf("UnfreezeAll failed: %v", err)
				}
			}
		}()
	}
	sameSetWG.Wait()

	// Barrier-controlled singleflight cleanup failure & stale duplicate interleaving test:
	// Worker 42 lease2 + shallow copy race CloseAndRelease while CloseIdle & Acquire attempt overlap.
	errInjected := errors.New("injected close failure")
	var failNext atomic.Bool
	failNext.Store(true)
	var cleanupCalls atomic.Int32
	cleanupEntered := make(chan struct{}, 1)
	cleanupRelease := make(chan struct{})
	var releaseGateOnce sync.Once
	releaseGate := func() {
		releaseGateOnce.Do(func() { close(cleanupRelease) })
	}
	t.Cleanup(releaseGate)
	waiterEntered := make(chan struct{}, 1)
	pool.waiterHook = func() {
		select {
		case waiterEntered <- struct{}{}:
		default:
		}
	}
	pool.closeSetHook = func(workerID int, set *WorkerArenaSet) error {
		if workerID == 42 && failNext.Load() {
			cleanupCalls.Add(1)
			select {
			case cleanupEntered <- struct{}{}:
			default:
			}
			<-cleanupRelease
			return errInjected
		}
		return set.Close()
	}

	leaseCopy := *lease2
	var dupErr1, dupErr2 error
	var dupWG sync.WaitGroup
	dupWG.Add(1)
	go func() {
		defer dupWG.Done()
		dupErr1 = lease2.CloseAndRelease()
	}()
	select {
	case <-cleanupEntered:
	case <-time.After(2 * time.Second):
		t.Fatalf("primary CloseAndRelease did not enter cleanup hook")
	}

	// While cleanup is blocked: duplicate concurrent CloseAndRelease must enter waiter path,
	// and bounded AcquireArenaLease(42) / CloseIdle probes must complete without blocking on pool mutex.
	dupWG.Add(1)
	go func() {
		defer dupWG.Done()
		dupErr2 = leaseCopy.CloseAndRelease()
	}()
	select {
	case <-waiterEntered:
	case <-time.After(2 * time.Second):
		t.Fatalf("duplicate CloseAndRelease did not enter concurrent waiter path")
	}

	acqProbeDone := make(chan error, 1)
	go func() {
		_, err := pool.AcquireArenaLease(42)
		acqProbeDone <- err
	}()
	select {
	case err := <-acqProbeDone:
		if err == nil {
			t.Fatalf("expected AcquireArenaLease(42) to fail while cleanup is in progress")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("AcquireArenaLease(42) probe timed out while cleanup was blocked")
	}

	idleProbeDone := make(chan error, 1)
	go func() {
		idleProbeDone <- pool.CloseIdle()
	}()
	select {
	case err := <-idleProbeDone:
		if err != nil {
			t.Fatalf("CloseIdle should skip worker 42 while cleanup is in progress, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("CloseIdle probe timed out while cleanup was blocked")
	}

	releaseGate()
	waitDone := make(chan struct{})
	go func() {
		dupWG.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatalf("dupWG timed out waiting for concurrent CloseAndRelease calls")
	}
	if !errors.Is(dupErr1, errInjected) || !errors.Is(dupErr2, errInjected) {
		t.Fatalf("expected both concurrent callers to receive errInjected, got (%v, %v)", dupErr1, dupErr2)
	}
	if calls := cleanupCalls.Load(); calls != 1 {
		t.Fatalf("expected singleflight cleanup count=1, got %d", calls)
	}

	// Dirty set must refuse reacquisition while retry cleanup still fails.
	if _, err := pool.AcquireArenaLease(42); !errors.Is(err, errInjected) {
		t.Fatalf("expected dirty worker 42 retry failure with errInjected, got %v", err)
	}

	// Allow cleanup retry to succeed and acquire lease3; stale leaseCopy.CloseAndRelease must still
	// return the recorded failure for lease2 without corrupting lease3.
	failNext.Store(false)
	lease3, err := pool.AcquireArenaLease(42)
	if err != nil || lease3 == nil {
		t.Fatalf("reacquire lease3 after dirty cleanup succeeded failed: %v", err)
	}
	if _, err := lease3.Set.GetOrCreateArena("mix", 4096); err != nil {
		t.Fatalf("lease3 GetOrCreateArena failed: %v", err)
	}
	if staleErr := leaseCopy.CloseAndRelease(); !errors.Is(staleErr, errInjected) {
		t.Fatalf("expected stale leaseCopy.CloseAndRelease to preserve recorded errInjected, got %v", staleErr)
	}
	if err := lease3.Set.VerifyIntegrity([]string{"mix"}); err != nil {
		t.Fatalf("stale leaseCopy corrupted active lease3 arenas: %v", err)
	}

	// Pool.Close must reject new acquisitions immediately and wait until active lease3 releases.
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- pool.Close()
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		tmpLease, err := pool.AcquireArenaLease(99)
		if err != nil {
			break
		}
		if err := tmpLease.CloseAndRelease(); err != nil {
			t.Fatalf("unexpected CloseAndRelease failure on probe lease: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("pool.Close did not reject new acquisitions promptly")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-closeDone:
		t.Fatalf("pool.Close finished prematurely before active lease3 released: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	if err := lease3.CloseAndRelease(); err != nil {
		t.Fatalf("lease3 CloseAndRelease failed: %v", err)
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("pool.Close failed after active lease released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("pool.Close timed out after active lease released")
	}

	// Multi-worker concurrent acquire / write / freeze / unfreeze / close stress.
	stressPool := NewShmArenaPool(false)
	defer stressPool.Close()
	const workers = 12
	const iterations = 20
	var wg sync.WaitGroup
	for w := 1; w <= workers; w++ {
		workerID := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				lease, err := stressPool.AcquireArenaLease(workerID)
				if err != nil {
					t.Errorf("worker %d iter %d acquire failed: %v", workerID, i, err)
					return
				}
				if _, err := lease.Set.GetOrCreateArena("mix", 4096); err != nil {
					t.Errorf("worker %d iter %d create arena failed: %v", workerID, i, err)
				}
				if err := lease.Set.FreezeStem("mix"); err != nil {
					t.Errorf("worker %d iter %d FreezeStem failed: %v", workerID, i, err)
				}
				_ = lease.Set.GetTagsMap()
				if err := lease.Set.UnfreezeAll(); err != nil {
					t.Errorf("worker %d iter %d UnfreezeAll failed: %v", workerID, i, err)
				}
				if err := lease.CloseAndRelease(); err != nil {
					t.Errorf("worker %d iter %d CloseAndRelease failed: %v", workerID, i, err)
				}
			}
		}()
	}
	wg.Wait()

	if err := stressPool.Close(); err != nil {
		t.Fatalf("final stressPool.Close() failed: %v", err)
	}
}
