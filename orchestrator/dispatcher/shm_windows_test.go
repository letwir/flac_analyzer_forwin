package dispatcher

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedMemory(t *testing.T) {
	name := "Local\\TestSHM123"
	size := uint32(1024)

	shm, err := NewSharedMemory(name, size)
	if err != nil {
		t.Fatalf("Failed to create shared memory: %v", err)
	}
	defer shm.Close()

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
	if err != nil {
		t.Fatalf("Failed to create shared memory: %v", err)
	}
	defer shm.Close()

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
	if err != nil {
		t.Fatalf("Failed to create shared memory: %v", err)
	}
	defer shm.Close()

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
	defer pool.Close()

	worker1 := pool.GetWorkerArenaSet(1)
	if worker1 == nil {
		t.Fatalf("Expected worker1 arena set, got nil")
	}

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

	pool.Close()
	if len(worker1.arenas) != 0 {
		t.Fatalf("Expected 0 arenas after pool close, got %d", len(worker1.arenas))
	}
}

func TestShmPythonInterop(t *testing.T) {
	name := "Local\\TestPythonInterop_mix"
	size := uint32(1024 * 1024 * 4) // 4MB

	shm, err := NewSharedMemory(name, size)
	if err != nil {
		t.Fatalf("Failed to create shared memory: %v", err)
	}
	defer shm.Close()

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

