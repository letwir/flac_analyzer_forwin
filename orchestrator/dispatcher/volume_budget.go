package dispatcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"flac_analyzer/orchestrator/sysinfo"
)

// ErrVolumeBudgetExhausted is returned when reserving bytes on a volume would
// breach the configured minimum free-space guard alongside active leases.
var ErrVolumeBudgetExhausted = errors.New("volume byte budget exhausted")

// VolumeLease is an at-most-once reservation token for bytes on a volume.
type VolumeLease struct {
	Token     uint64
	VolumeKey string
	Bytes     uint64
}

type volumeLeaseState struct {
	volumeKey string
	bytes     uint64
}

// VolumeBudgetLedger tracks concurrent byte reservations per physical volume
// so DiskCacheDir, SpectroCacheDir, and temporary files sharing the same drive
// cannot overcommit available space.
type VolumeBudgetLedger struct {
	mu           sync.Mutex
	reserved     map[string]uint64
	leases       map[uint64]volumeLeaseState
	nextID       uint64
	freeSpaceFn  func(string) (uint64, error)
	resolveKeyFn func(string) (string, error)
}

func defaultVolumeFreeSpace(dirPath string) (uint64, error) {
	info, err := sysinfo.GetDiskFreeSpace(dirPath)
	if err != nil {
		return 0, err
	}
	if info == nil {
		return 0, fmt.Errorf("disk free space observation unavailable for %q", dirPath)
	}
	return info.FreeBytesAvailable, nil
}

// NewVolumeBudgetLedger constructs a per-volume byte reservation ledger.
func NewVolumeBudgetLedger(freeSpaceFn func(string) (uint64, error)) *VolumeBudgetLedger {
	if freeSpaceFn == nil {
		freeSpaceFn = defaultVolumeFreeSpace
	}
	return &VolumeBudgetLedger{
		reserved:     make(map[string]uint64),
		leases:       make(map[uint64]volumeLeaseState),
		freeSpaceFn:  freeSpaceFn,
		resolveKeyFn: resolveVolumeKeyAndAnchor,
	}
}

func resolveAnchorPath(rawPath string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", fmt.Errorf("empty path")
	}

	// Preserve '..' segments until symlinks in preceding components are evaluated,
	// avoiding lexical collapse of '/a/link/../cache' before symlink resolution.
	var base string
	var rest string
	if filepath.IsAbs(trimmed) {
		vol := filepath.VolumeName(trimmed)
		afterVol := strings.TrimPrefix(trimmed, vol)
		afterVol = strings.TrimLeft(afterVol, `/\`)
		if vol != "" {
			base = vol + string(filepath.Separator)
		} else {
			base = string(filepath.Separator)
		}
		rest = afterVol
	} else {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		if evalWd, err := filepath.EvalSymlinks(wd); err == nil && evalWd != "" {
			wd = evalWd
		}
		base = wd
		rest = trimmed
	}

	cur := base
	segments := strings.FieldsFunc(rest, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for _, seg := range segments {
		switch seg {
		case ".":
			continue
		case "..":
			if eval, err := filepath.EvalSymlinks(cur); err == nil && eval != "" {
				cur = eval
			}
			cur = filepath.Dir(cur)
		default:
			candidate := filepath.Join(cur, seg)
			if eval, err := filepath.EvalSymlinks(candidate); err == nil && eval != "" {
				cur = eval
			} else {
				cur = candidate
			}
		}
	}

	return nearestExistingAncestor(cur)
}

func nearestExistingAncestor(path string) (string, error) {
	cur := path
	for {
		_, err := os.Stat(cur)
		if err == nil {
			if eval, evalErr := filepath.EvalSymlinks(cur); evalErr == nil && eval != "" {
				return eval, nil
			}
			return cur, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat ancestor %q: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		cur = parent
	}
}

func resolveVolumeKeyAndAnchor(path string) (string, error) {
	anchor, err := resolveAnchorPath(path)
	if err != nil {
		return "", err
	}
	return resolveVolumeIdentity(anchor)
}

// VolumeKeyE resolves path (via component-ordered symlink traversal and its
// nearest existing ancestor) to a canonical OS filesystem/volume identity.
func VolumeKeyE(path string) (string, error) {
	return resolveVolumeKeyAndAnchor(path)
}

// VolumeKey returns the canonical volume identity or "" if resolution fails.
func VolumeKey(path string) string {
	k, err := resolveVolumeKeyAndAnchor(path)
	if err != nil {
		return ""
	}
	return k
}

func (l *VolumeBudgetLedger) resolveKey(dirPath string) (string, string, error) {
	anchor, anchorErr := resolveAnchorPath(dirPath)
	if anchorErr != nil {
		return "", "", anchorErr
	}
	if l != nil && l.resolveKeyFn != nil {
		key, err := l.resolveKeyFn(anchor)
		if err != nil {
			return "", "", err
		}
		return key, anchor, nil
	}
	key, err := resolveVolumeIdentity(anchor)
	if err != nil {
		return "", "", err
	}
	return key, anchor, nil
}

func (l *VolumeBudgetLedger) probePath(dirPath string, exclude VolumeLease) (volKey string, avail uint64, reserved uint64, err error) {
	volKey, anchor, err := l.resolveKey(dirPath)
	if err != nil || volKey == "" {
		if err == nil {
			err = fmt.Errorf("empty canonical volume key")
		}
		return "", 0, 0, err
	}
	freeFn := defaultVolumeFreeSpace
	if l != nil && l.freeSpaceFn != nil {
		freeFn = l.freeSpaceFn
	}
	avail, err = freeFn(anchor)
	if err != nil {
		return "", 0, 0, err
	}
	if l != nil {
		l.mu.Lock()
		reserved = l.reserved[volKey]
		if exclude.Token != 0 {
			if st, ok := l.leases[exclude.Token]; ok && st.volumeKey == volKey {
				reserved = safeSub(reserved, st.bytes)
			}
		}
		l.mu.Unlock()
	}
	return volKey, avail, reserved, nil
}

// Reserve atomically reserves reqBytes on dirPath's volume while preserving at
// least minFreeBytes of free space above all active reservations on that volume.
func (l *VolumeBudgetLedger) Reserve(dirPath string, reqBytes, minFreeBytes uint64) (VolumeLease, error) {
	if l == nil {
		return VolumeLease{}, fmt.Errorf("%w: nil ledger", ErrVolumeBudgetExhausted)
	}
	trimmed := strings.TrimSpace(dirPath)
	if trimmed == "" {
		return VolumeLease{}, fmt.Errorf("%w: empty directory path", ErrVolumeBudgetExhausted)
	}
	volKey, anchor, err := l.resolveKey(trimmed)
	if err != nil || volKey == "" {
		if err == nil {
			err = fmt.Errorf("empty canonical volume key")
		}
		return VolumeLease{}, fmt.Errorf("resolve canonical volume identity (%q): %w", dirPath, err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	freeFn := l.freeSpaceFn
	if freeFn == nil {
		freeFn = defaultVolumeFreeSpace
	}
	avail, err := freeFn(anchor)
	if err != nil {
		return VolumeLease{}, fmt.Errorf("observe volume free space (%s): %w", volKey, err)
	}

	curReserved := l.reserved[volKey]
	nextReserved, ok1 := checkedAddUint64(curReserved, reqBytes)
	totalRequired, ok2 := checkedAddUint64(nextReserved, minFreeBytes)
	if !ok1 || !ok2 || avail < totalRequired {
		return VolumeLease{}, fmt.Errorf(
			"%w: volume=%s avail=%d reserved=%d req=%d min_free=%d",
			ErrVolumeBudgetExhausted,
			volKey,
			avail,
			curReserved,
			reqBytes,
			minFreeBytes,
		)
	}

	l.nextID++
	if l.nextID == 0 {
		return VolumeLease{}, fmt.Errorf("%w: lease token wrapped", ErrVolumeBudgetExhausted)
	}
	if l.reserved == nil {
		l.reserved = make(map[string]uint64)
	}
	if l.leases == nil {
		l.leases = make(map[uint64]volumeLeaseState)
	}
	l.reserved[volKey] = nextReserved
	l.leases[l.nextID] = volumeLeaseState{volumeKey: volKey, bytes: reqBytes}
	return VolumeLease{
		Token:     l.nextID,
		VolumeKey: volKey,
		Bytes:     reqBytes,
	}, nil
}

// Release releases a previously acquired VolumeLease at most once.
func (l *VolumeBudgetLedger) Release(lease VolumeLease) bool {
	if l == nil || lease.Token == 0 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.leases[lease.Token]
	if !ok {
		return false
	}
	next := safeSub(l.reserved[state.volumeKey], state.bytes)
	if next == 0 {
		delete(l.reserved, state.volumeKey)
	} else {
		l.reserved[state.volumeKey] = next
	}
	delete(l.leases, lease.Token)
	return true
}

// ReservedBytes returns the currently reserved byte total for dirPath's volume.
func (l *VolumeBudgetLedger) ReservedBytes(dirPath string) uint64 {
	n, err := l.ReservedBytesExcludingE(dirPath, VolumeLease{})
	if err != nil {
		return 0
	}
	return n
}

// ReservedBytesExcluding returns reserved bytes on dirPath's volume excluding an
// active caller-held lease (used when rechecking an already-admitted task).
func (l *VolumeBudgetLedger) ReservedBytesExcluding(dirPath string, exclude VolumeLease) uint64 {
	n, err := l.ReservedBytesExcludingE(dirPath, exclude)
	if err != nil {
		return 0
	}
	return n
}

// ReservedBytesExcludingE resolves dirPath's canonical volume key and returns
// currently reserved bytes excluding exclude, propagating any identity error.
func (l *VolumeBudgetLedger) ReservedBytesExcludingE(dirPath string, exclude VolumeLease) (uint64, error) {
	if l == nil {
		return 0, nil
	}
	volKey, _, err := l.resolveKey(dirPath)
	if err != nil || volKey == "" {
		if err == nil {
			err = fmt.Errorf("empty canonical volume key")
		}
		return 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	total := l.reserved[volKey]
	if exclude.Token != 0 {
		if st, ok := l.leases[exclude.Token]; ok && st.volumeKey == volKey {
			total = safeSub(total, st.bytes)
		}
	}
	return total, nil
}

// ReleaseAfterCleanup executes cleanup and releases the volume lease ONLY when
// cleanup returns nil so failed cleanups retain their reservation for retry.
func (l *VolumeBudgetLedger) ReleaseAfterCleanup(lease VolumeLease, cleanup func() error) error {
	if cleanup == nil {
		return fmt.Errorf("cleanup callback is required")
	}
	if err := cleanup(); err != nil {
		return err
	}
	if l != nil && lease.Token != 0 {
		l.Release(lease)
	}
	return nil
}
