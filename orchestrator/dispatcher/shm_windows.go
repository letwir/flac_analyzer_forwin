package dispatcher

import (
	"errors"
	"fmt"
	"log"
	"os"
	"reflect"
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileMappingW         = kernel32.NewProc("CreateFileMappingW")
	procMapViewOfFile              = kernel32.NewProc("MapViewOfFile")
	procUnmapViewOfFile            = kernel32.NewProc("UnmapViewOfFile")
	procVirtualProtect             = kernel32.NewProc("VirtualProtect")
	procGlobalMemoryStatusEx       = kernel32.NewProc("GlobalMemoryStatusEx")
	procVirtualLock                = kernel32.NewProc("VirtualLock")
	procVirtualUnlock              = kernel32.NewProc("VirtualUnlock")
	procGetProcessWorkingSetSizeEx = kernel32.NewProc("GetProcessWorkingSetSizeEx")
	procSetProcessWorkingSetSizeEx = kernel32.NewProc("SetProcessWorkingSetSizeEx")
	procGetCurrentProcess          = kernel32.NewProc("GetCurrentProcess")
)

const (
	PAGE_READWRITE = 0x04
	PAGE_READONLY  = 0x02
	FILE_MAP_WRITE = 0x0002
	FILE_MAP_READ  = 0x0004

	// Win32 Working Set Quota Flags
	QUOTA_LIMITS_HARDWS_MIN_ENABLE  = 0x00000001
	QUOTA_LIMITS_HARDWS_MIN_DISABLE = 0x00000002
	QUOTA_LIMITS_HARDWS_MAX_ENABLE  = 0x00000004
	QUOTA_LIMITS_HARDWS_MAX_DISABLE = 0x00000008

	// Win32 Error Codes
	ERROR_NOT_ENOUGH_MEMORY = 8
	ERROR_WORKING_SET_QUOTA = 1453
)

type MemoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func GetAvailableMemory() (uint64, error) {
	var memStatus MemoryStatusEx
	memStatus.Length = uint32(unsafe.Sizeof(memStatus))
	ret, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&memStatus)))
	if ret == 0 {
		return 0, err
	}
	return memStatus.AvailPhys, nil
}

// GetProcessWorkingSetSize retrieves the current working set quotas for the current process.
func GetProcessWorkingSetSize() (minSize, maxSize uintptr, flags uint32, err error) {
	hProc, _, _ := procGetCurrentProcess.Call()
	if hProc == 0 {
		return 0, 0, 0, fmt.Errorf("GetCurrentProcess failed")
	}
	ret, _, errStr := procGetProcessWorkingSetSizeEx.Call(
		hProc,
		uintptr(unsafe.Pointer(&minSize)),
		uintptr(unsafe.Pointer(&maxSize)),
		uintptr(unsafe.Pointer(&flags)),
	)
	if ret == 0 {
		return 0, 0, 0, fmt.Errorf("GetProcessWorkingSetSizeEx failed: %v", errStr)
	}
	return minSize, maxSize, flags, nil
}

// SetProcessWorkingSetSize sets minimum and maximum working set sizes with specified flags.
func SetProcessWorkingSetSize(minBytes, maxBytes uintptr, flags uint32) error {
	hProc, _, _ := procGetCurrentProcess.Call()
	if hProc == 0 {
		return fmt.Errorf("GetCurrentProcess failed")
	}
	ret, _, errStr := procSetProcessWorkingSetSizeEx.Call(hProc, minBytes, maxBytes, uintptr(flags))
	if ret == 0 {
		// Fallback: If specific flags failed (e.g. HARDWS flags without privilege), try standard flags = 0
		if flags != 0 {
			retRetry, _, errRetry := procSetProcessWorkingSetSizeEx.Call(hProc, minBytes, maxBytes, 0)
			if retRetry != 0 {
				return nil
			}
			return fmt.Errorf("SetProcessWorkingSetSizeEx failed (flags 0x%x: %v, flags 0: %v)", flags, errStr, errRetry)
		}
		return fmt.Errorf("SetProcessWorkingSetSizeEx failed: %v", errStr)
	}
	return nil
}

// EnableProcessWorkingSetLock expands working set for initial physical RAM locking.
func EnableProcessWorkingSetLock(minMB, maxMB int) error {
	if minMB <= 0 {
		minMB = 512
	}
	if maxMB <= minMB {
		maxMB = minMB * 4
	}
	minBytes := uintptr(minMB) * 1024 * 1024
	maxBytes := uintptr(maxMB) * 1024 * 1024
	// Try HARDWS_MIN_ENABLE | HARDWS_MAX_DISABLE
	flags := uint32(QUOTA_LIMITS_HARDWS_MIN_ENABLE | QUOTA_LIMITS_HARDWS_MAX_DISABLE)
	return SetProcessWorkingSetSize(minBytes, maxBytes, flags)
}

// ExpandWorkingSetForSize dynamically expands working set quotas to accommodate requiredBytes.
func ExpandWorkingSetForSize(requiredBytes uintptr) error {
	currMin, currMax, currFlags, err := GetProcessWorkingSetSize()
	var newMin, newMax uintptr
	margin := uintptr(64 * 1024 * 1024)

	if err == nil && currMin > 0 && currMax > 0 {
		newMin = currMin + requiredBytes + margin
		newMax = currMax + (requiredBytes * 2) + margin
		if newMax < newMin*2 {
			newMax = newMin * 2
		}
	} else {
		newMin = (requiredBytes * 2) + uintptr(128*1024*1024)
		newMax = newMin * 4
		currFlags = uint32(QUOTA_LIMITS_HARDWS_MIN_ENABLE | QUOTA_LIMITS_HARDWS_MAX_DISABLE)
	}

	return SetProcessWorkingSetSize(newMin, newMax, currFlags)
}

// LockMemory attempts to pin virtual memory pages to physical RAM via VirtualLock.
// If ERROR_WORKING_SET_QUOTA is encountered, it automatically expands the working set and retries.
func LockMemory(addr uintptr, size uintptr) (bool, error) {
	if addr == 0 || size == 0 {
		return false, nil
	}

	const maxRetries = 3
	var lastErr error

	for attempt := 1; attempt <= maxRetries; attempt++ {
		retLock, _, errLock := procVirtualLock.Call(addr, size)
		if retLock != 0 {
			return true, nil
		}

		lastErr = errLock
		errno, ok := errLock.(syscall.Errno)
		if ok && (errno == syscall.Errno(ERROR_WORKING_SET_QUOTA) || errno == syscall.Errno(ERROR_NOT_ENOUGH_MEMORY)) {
			// Expand working set quota and retry
			expandAmount := size * uintptr(attempt)
			if expErr := ExpandWorkingSetForSize(expandAmount); expErr != nil {
				// Try expanding with basic flags = 0
				_ = SetProcessWorkingSetSize(expandAmount*2+uintptr(64*1024*1024), expandAmount*4+uintptr(256*1024*1024), 0)
			}
			continue
		}
		// For non-quota errors, break early
		break
	}

	return false, lastErr
}

// UnlockMemory unpins virtual memory pages from physical RAM via VirtualUnlock.
func UnlockMemory(addr uintptr, size uintptr) error {
	if addr == 0 || size == 0 {
		return nil
	}
	retUnlock, _, errUnlock := procVirtualUnlock.Call(addr, size)
	if retUnlock == 0 {
		return fmt.Errorf("VirtualUnlock failed: %w", errUnlock)
	}
	return nil
}

type SharedMemory struct {
	Name       string
	Size       uint32
	handle     syscall.Handle
	addr       uintptr
	data       []byte
	isLocked   bool
	enableLock bool
}

func NewSharedMemory(name string, size uint32) (*SharedMemory, error) {
	return NewSharedMemoryWithLock(name, size, true)
}

// NewSharedMemoryWithLock creates and maps a named Windows shared memory region.
// If MapViewOfFile and rollback CloseHandle both fail, it returns a partial *SharedMemory
// retaining the open handle so callers can retry Close() without leaking handles.
func NewSharedMemoryWithLock(name string, size uint32, enableLock bool) (*SharedMemory, error) {
	name16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}

	handle, _, errStr := procCreateFileMappingW.Call(
		uintptr(syscall.InvalidHandle),
		0,
		PAGE_READWRITE,
		0,
		uintptr(size),
		uintptr(unsafe.Pointer(name16)),
	)
	if handle == 0 {
		return nil, fmt.Errorf("CreateFileMappingW failed: %w", errStr)
	}

	addr, _, errStr := procMapViewOfFile.Call(
		handle,
		FILE_MAP_WRITE|FILE_MAP_READ,
		0,
		0,
		uintptr(size),
	)
	if addr == 0 {
		mapErr := fmt.Errorf("MapViewOfFile failed: %w", errStr)
		if closeErr := syscall.CloseHandle(syscall.Handle(handle)); closeErr != nil {
			partial := &SharedMemory{
				Name:       name,
				Size:       size,
				handle:     syscall.Handle(handle),
				enableLock: enableLock,
			}
			return partial, errors.Join(mapErr, fmt.Errorf("rollback CloseHandle failed: %w", closeErr))
		}
		return nil, mapErr
	}

	var data []byte
	header := (*reflect.SliceHeader)(unsafe.Pointer(&data))
	header.Data = addr
	header.Len = int(size)
	header.Cap = int(size)

	locked := false
	if enableLock {
		var lockErr error
		locked, lockErr = LockMemory(addr, uintptr(size))
		if !locked {
			// 失敗時は警告ログを出力し、通常のページファイルバッキング共有メモリへフォールバックしますの
			log.Printf("[WARN] VirtualLock failed for SHM %s (size %d): %v. Fallback to standard shared memory.", name, size, lockErr)
		}
	}

	return &SharedMemory{
		Name:       name,
		Size:       size,
		handle:     syscall.Handle(handle),
		addr:       addr,
		data:       data,
		isLocked:   locked,
		enableLock: enableLock,
	}, nil
}

func (shm *SharedMemory) Write(data []byte) error {
	if len(data) > int(shm.Size) {
		return fmt.Errorf("data size %d exceeds shared memory size %d", len(data), shm.Size)
	}
	copy(shm.data, data)
	return nil
}

func (shm *SharedMemory) Freeze() error {
	var oldProtect uint32
	ret, _, errStr := procVirtualProtect.Call(
		shm.addr,
		uintptr(shm.Size),
		PAGE_READONLY,
		uintptr(unsafe.Pointer(&oldProtect)),
	)
	if ret == 0 {
		return fmt.Errorf("VirtualProtect(PAGE_READONLY) failed: %w", errStr)
	}
	return nil
}

func (shm *SharedMemory) Unfreeze() error {
	var oldProtect uint32
	ret, _, errStr := procVirtualProtect.Call(
		shm.addr,
		uintptr(shm.Size),
		PAGE_READWRITE,
		uintptr(unsafe.Pointer(&oldProtect)),
	)
	if ret == 0 {
		return fmt.Errorf("VirtualProtect(PAGE_READWRITE) failed: %w", errStr)
	}
	return nil
}

func (shm *SharedMemory) EnsureCapacity(requiredSize uint32) error {
	if shm.Size >= requiredSize && shm.handle != 0 && shm.addr != 0 {
		// 既存の領域で十分収まる場合、未ロックであればロックを試行し、Unfreeze (PAGE_READWRITE) して即座に再利用しますわ！
		if shm.enableLock && !shm.isLocked {
			locked, _ := LockMemory(shm.addr, uintptr(shm.Size))
			shm.isLocked = locked
		}
		return shm.Unfreeze()
	}

	name := shm.Name
	enableLock := shm.enableLock
	if err := shm.Close(); err != nil {
		return fmt.Errorf("close existing SHM before resize %s: %w", name, err)
	}

	newShm, err := NewSharedMemoryWithLock(name, requiredSize, enableLock)
	if err != nil {
		if newShm != nil {
			*shm = *newShm
		}
		return err
	}

	*shm = *newShm
	return nil
}

func (shm *SharedMemory) Close() error {
	var closeErrs []error
	if shm.addr != 0 {
		if shm.isLocked {
			if err := UnlockMemory(shm.addr, uintptr(shm.Size)); err != nil {
				closeErrs = append(closeErrs, fmt.Errorf("VirtualUnlock failed: %w", err))
			} else {
				shm.isLocked = false
			}
		}
		ret, _, errStr := procUnmapViewOfFile.Call(shm.addr)
		if ret == 0 {
			closeErrs = append(closeErrs, fmt.Errorf("UnmapViewOfFile failed: %w", errStr))
		} else {
			shm.addr = 0
			shm.data = nil
			shm.isLocked = false
		}
	}
	if shm.handle != 0 {
		if err := syscall.CloseHandle(shm.handle); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("CloseHandle failed: %w", err))
		} else {
			shm.handle = 0
		}
	}
	return errors.Join(closeErrs...)
}

type WorkerArenaSet struct {
	mu         sync.Mutex
	WorkerID   int
	arenas     map[string]*SharedMemory
	enableLock bool
	dirty      bool
}

func NewWorkerArenaSet(workerID int, enableLock bool) *WorkerArenaSet {
	return &WorkerArenaSet{
		WorkerID:   workerID,
		arenas:     make(map[string]*SharedMemory),
		enableLock: enableLock,
	}
}

func (w *WorkerArenaSet) GetOrCreateArena(stem string, requiredSize uint32) (*SharedMemory, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	shm, exists := w.arenas[stem]
	if !exists {
		tagName := fmt.Sprintf("Local\\FlacShm_W%d_%s", w.WorkerID, stem)
		newShm, err := NewSharedMemoryWithLock(tagName, requiredSize, w.enableLock)
		if err != nil {
			if newShm != nil {
				w.arenas[stem] = newShm
				w.dirty = true
			}
			return nil, fmt.Errorf("failed to create initial SHM arena for stem %s (size %d): %w", stem, requiredSize, err)
		}
		w.arenas[stem] = newShm
		return newShm, nil
	}

	if err := shm.EnsureCapacity(requiredSize); err != nil {
		if shm.handle != 0 || shm.addr != 0 {
			w.dirty = true
		}
		return nil, fmt.Errorf("failed to ensure capacity for stem %s (size %d): %w", stem, requiredSize, err)
	}
	return shm, nil
}

func (w *WorkerArenaSet) FreezeStem(stem string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	arena, ok := w.arenas[stem]
	if !ok {
		return fmt.Errorf("missing SHM arena for published stem %q", stem)
	}
	if err := arena.Freeze(); err != nil {
		return fmt.Errorf("freeze published stem %q: %w", stem, err)
	}
	return nil
}

func (w *WorkerArenaSet) FreezeAll() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var freezeErrs []error
	for stem, shm := range w.arenas {
		if err := shm.Freeze(); err != nil {
			log.Printf("[WARN] [Worker %d] Failed to freeze SHM %s: %v", w.WorkerID, stem, err)
			freezeErrs = append(freezeErrs, fmt.Errorf("freeze SHM arena %s: %w", stem, err))
		}
	}
	return errors.Join(freezeErrs...)
}

func (w *WorkerArenaSet) UnfreezeAll() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var unfreezeErrs []error
	for stem, shm := range w.arenas {
		if err := shm.Unfreeze(); err != nil {
			log.Printf("[WARN] [Worker %d] Failed to unfreeze SHM %s: %v", w.WorkerID, stem, err)
			unfreezeErrs = append(unfreezeErrs, fmt.Errorf("unfreeze SHM arena %s: %w", stem, err))
		}
	}
	return errors.Join(unfreezeErrs...)
}

func (w *WorkerArenaSet) VerifyIntegrity(stems []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	for _, stem := range stems {
		shm, exists := w.arenas[stem]
		if !exists {
			return fmt.Errorf("stem %s arena does not exist for worker %d", stem, w.WorkerID)
		}
		if shm.handle == 0 || shm.addr == 0 || shm.Size == 0 {
			return fmt.Errorf("stem %s arena is invalid (handle: %v, addr: 0x%x, size: %d)", stem, shm.handle, shm.addr, shm.Size)
		}
	}
	return nil
}

// ExtractFlacStreaminfoMD5 extracts PCM MD5 signature directly from FLAC STREAMINFO header (34-byte block).
// Returns empty string with error if file is invalid, not FLAC, or MD5 is all zeroes.
func ExtractFlacStreaminfoMD5(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	header := make([]byte, 42) // 4 byte magic + 4 byte block header + 34 byte STREAMINFO
	n, err := f.Read(header)
	if err != nil || n < 42 {
		return "", fmt.Errorf("failed to read FLAC header: %w", err)
	}

	// Verify "fLaC" magic (0x66, 0x4C, 0x61, 0x43)
	if header[0] != 'f' || header[1] != 'L' || header[2] != 'a' || header[3] != 'C' {
		return "", fmt.Errorf("not a valid FLAC file (magic mismatch)")
	}

	// Check block type (first 7 bits of byte 4 should be 0 for STREAMINFO)
	blockType := header[4] & 0x7F
	if blockType != 0 {
		return "", fmt.Errorf("first metadata block is not STREAMINFO (type: %d)", blockType)
	}

	// STREAMINFO MD5 signature is bytes 26 to 41 (16 bytes)
	md5Bytes := header[26:42]
	allZero := true
	for _, b := range md5Bytes {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return "", fmt.Errorf("STREAMINFO MD5 is uninitialized (all zeros)")
	}

	return fmt.Sprintf("%x", md5Bytes), nil
}

func (w *WorkerArenaSet) GetTagsMap() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()

	tags := make(map[string]string, len(w.arenas))
	for stem, shm := range w.arenas {
		tags[stem] = shm.Name
	}
	return tags
}

func (w *WorkerArenaSet) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var closeErrs []error
	for stem, shm := range w.arenas {
		if err := shm.Close(); err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("close SHM arena %s: %w", stem, err))
			continue
		}
		delete(w.arenas, stem)
	}
	return errors.Join(closeErrs...)
}

// ArenaLease represents a uniquely-tokenized exclusive borrow of a WorkerArenaSet.
// It is safe to copy by value; all copies share the underlying singleflight lease state.
type ArenaLease struct {
	pool  *ShmArenaPool
	Set   *WorkerArenaSet
	state *arenaLeaseState
}

// CloseAndRelease closes the leased arenas and releases the lease exactly once.
// Concurrent or duplicate calls on the lease or copies share the single cleanup result.
func (l *ArenaLease) CloseAndRelease() error {
	if l == nil || l.pool == nil || l.state == nil {
		return nil
	}
	return l.pool.releaseLeaseState(l.state)
}

type arenaLeaseState struct {
	workerID int
	token    uint64
	set      *WorkerArenaSet
	closing  bool
	done     bool
	err      error
	waitCh   chan struct{}
}

type ShmArenaPool struct {
	mu           sync.Mutex
	cond         *sync.Cond
	workers      map[int]*WorkerArenaSet
	active       map[int]*arenaLeaseState
	nextToken    uint64
	closed       bool
	enableLock   bool
	closeSetHook func(workerID int, set *WorkerArenaSet) error
	waiterHook   func()
}

func NewShmArenaPool(enableLock bool) *ShmArenaPool {
	p := &ShmArenaPool{
		workers:    make(map[int]*WorkerArenaSet),
		active:     make(map[int]*arenaLeaseState),
		enableLock: enableLock,
	}
	p.cond = sync.NewCond(&p.mu)
	return p
}

func (p *ShmArenaPool) closeWorkerSet(workerID int, set *WorkerArenaSet) error {
	if set == nil {
		return nil
	}
	if p.closeSetHook != nil {
		return p.closeSetHook(workerID, set)
	}
	return set.Close()
}

// AcquireArenaLease leases workerID's arena set with a monotonic ownership token.
func (p *ShmArenaPool) AcquireArenaLease(workerID int) (*ArenaLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, fmt.Errorf("shm arena pool is closed")
	}
	if _, inUse := p.active[workerID]; inUse {
		return nil, fmt.Errorf("worker arena set %d is already in use", workerID)
	}
	set, exists := p.workers[workerID]
	if exists && set.dirty {
		if err := p.closeWorkerSet(workerID, set); err != nil {
			return nil, fmt.Errorf("retry cleanup of dirty worker arena set %d: %w", workerID, err)
		}
		set.dirty = false
	}
	if !exists {
		set = NewWorkerArenaSet(workerID, p.enableLock)
		p.workers[workerID] = set
	}
	p.nextToken++
	if p.nextToken == 0 {
		p.nextToken = 1
	}
	tok := p.nextToken
	st := &arenaLeaseState{
		workerID: workerID,
		token:    tok,
		set:      set,
		waitCh:   make(chan struct{}),
	}
	p.active[workerID] = st
	return &ArenaLease{
		pool:  p,
		Set:   set,
		state: st,
	}, nil
}

func (p *ShmArenaPool) releaseLeaseState(st *arenaLeaseState) error {
	p.mu.Lock()
	if st.done {
		err := st.err
		p.mu.Unlock()
		return err
	}
	if st.closing {
		waitCh := st.waitCh
		hook := p.waiterHook
		p.mu.Unlock()
		if hook != nil {
			hook()
		}
		<-waitCh
		return st.err
	}
	activeSt, ok := p.active[st.workerID]
	if !ok || activeSt != st {
		st.done = true
		p.mu.Unlock()
		return st.err
	}
	st.closing = true
	set := st.set
	p.mu.Unlock()

	closeErr := p.closeWorkerSet(st.workerID, set)

	p.mu.Lock()
	st.err = closeErr
	st.closing = false
	st.done = true
	if closeErr != nil && set != nil {
		set.dirty = true
	}
	delete(p.active, st.workerID)
	close(st.waitCh)
	if p.cond != nil {
		p.cond.Broadcast()
	}
	p.mu.Unlock()
	return closeErr
}

// CloseIdle closes any non-leased worker arena sets while leaving the pool open.
func (p *ShmArenaPool) CloseIdle() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var closeErrs []error
	for id, set := range p.workers {
		if _, inUse := p.active[id]; inUse {
			continue
		}
		if err := set.Close(); err != nil {
			set.dirty = true
			closeErrs = append(closeErrs, fmt.Errorf("close idle worker arena %d: %w", id, err))
			continue
		}
		set.dirty = false
		delete(p.workers, id)
	}
	return errors.Join(closeErrs...)
}

// Close stops new acquisitions, drains active leases, and closes all worker arena sets.
func (p *ShmArenaPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true
	for len(p.active) > 0 {
		p.cond.Wait()
	}

	var closeErrs []error
	for id, set := range p.workers {
		if err := set.Close(); err != nil {
			set.dirty = true
			closeErrs = append(closeErrs, fmt.Errorf("close worker arena %d: %w", id, err))
			continue
		}
		set.dirty = false
		delete(p.workers, id)
	}
	return errors.Join(closeErrs...)
}
