// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// SideEffectFn: Request-scoped cache directory ownership & containment
package dispatcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const (
	cacheOwnerFileName = "owner.json"
	cacheRootDirName   = "flac_analyzer_cache"

	// Reserved namespaces. A request directory is named "req-<id>" and becomes visible only
	// after owner.json exists: it is built as "creating-<id>" and renamed atomically. Removal
	// first renames the directory to "removing-<nonce>-<id>" (atomic, receipt travels with it)
	// so concurrent creators and sweeps can never act on a path that changed identity.
	requestDirPrefix  = "req-"
	creatingDirPrefix = "creating-"
	removingDirPrefix = "removing-"
)

// requestIDPattern admits only a dot-free "req-" element: no separators, no trailing dots
// or spaces (Windows path aliases), and the fixed prefix rules out reserved device names.
var requestIDPattern = regexp.MustCompile(`^req-[A-Za-z0-9][A-Za-z0-9_-]{0,120}$`)

// cacheOwner is the ownership receipt stored in <dir>/owner.json. PID alone is not
// enough to prove liveness (PIDs are reused), so the owner's process start time is
// recorded as well.
type cacheOwner struct {
	PID               int    `json:"pid"`
	StartTimeUnixNano int64  `json:"start_time_unix_nano"`
	RequestID         string `json:"request_id"`
	CreatedUnixNano   int64  `json:"created_unix_nano"`
}

// cacheOwnerAlive reports whether the owning process is still the same live process.
// It is a variable so tests can substitute deterministic liveness.
var cacheOwnerAlive = processMatchesOwner

// DefaultCacheBaseDir is the shared parent of all request-scoped cache directories.
func DefaultCacheBaseDir() string {
	return filepath.Join(os.TempDir(), cacheRootDirName)
}

// cacheRequestSeq disambiguates request IDs minted within the same nanosecond.
var cacheRequestSeq atomic.Uint64

// cacheQuarantineSeq disambiguates quarantine names minted within the same nanosecond.
var cacheQuarantineSeq atomic.Uint64

// NewCacheRequestID mints a unique, path-safe request ID for CreateRequestCacheDir.
func NewCacheRequestID() string {
	return fmt.Sprintf("%s%d-%d-%d", requestDirPrefix, os.Getpid(), time.Now().UnixNano(), cacheRequestSeq.Add(1))
}

// hasPrefixFold matches reserved prefixes case-insensitively: on Windows "REQ-x" aliases "req-x".
func hasPrefixFold(name, prefix string) bool {
	return len(name) >= len(prefix) && strings.EqualFold(name[:len(prefix)], prefix)
}

// quarantineName builds "removing-<nonce>-<id>"; the digits-only nonce keeps the embedded
// request ID ("req-...") recoverable.
func quarantineName(requestID string) string {
	return fmt.Sprintf("%s%d-%d-%s", removingDirPrefix, time.Now().UnixNano(), cacheQuarantineSeq.Add(1), requestID)
}

// requestIDFromReserved extracts the embedded request ID from a "creating-" or "removing-" name.
func requestIDFromReserved(name string) string {
	switch {
	case hasPrefixFold(name, creatingDirPrefix):
		return name[len(creatingDirPrefix):]
	case hasPrefixFold(name, removingDirPrefix):
		if i := strings.Index(name, "-"+requestDirPrefix); i >= 0 {
			return name[i+1:]
		}
	}
	return ""
}

func validateRequestID(requestID string) error {
	if !requestIDPattern.MatchString(requestID) {
		return fmt.Errorf("invalid cache request ID %q", requestID)
	}
	return nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// resolveCacheBase validates a cache base: non-empty, absolute-resolvable and not itself a
// reparse point. A missing base is reported via exists=false.
func resolveCacheBase(baseDir string) (base string, exists bool, err error) {
	if strings.TrimSpace(baseDir) == "" {
		return "", false, errors.New("cache base dir must be non-empty")
	}
	base, err = filepath.Abs(baseDir)
	if err != nil {
		return "", false, fmt.Errorf("resolve cache base dir: %w", err)
	}
	if _, statErr := os.Lstat(base); statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return base, false, nil
		}
		return "", false, fmt.Errorf("inspect cache base dir: %w", statErr)
	}
	reparse, err := isReparsePoint(base)
	if err != nil {
		return "", false, fmt.Errorf("inspect cache base dir: %w", err)
	}
	if reparse {
		return "", false, fmt.Errorf("cache base dir %q is a reparse point", base)
	}
	return base, true, nil
}

// resolveDirectChild returns the base and the absolute dirPath only if dirPath is exactly
// one valid request-ID element below baseDir.
func resolveDirectChild(baseDir, dirPath string) (string, string, error) {
	if strings.TrimSpace(dirPath) == "" {
		return "", "", errors.New("cache target dir must be non-empty")
	}
	base, _, err := resolveCacheBase(baseDir)
	if err != nil {
		return "", "", err
	}
	child, err := filepath.Abs(dirPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve cache dir: %w", err)
	}
	if !samePath(filepath.Dir(child), base) {
		return "", "", fmt.Errorf("cache dir %q is not a direct child of %q", child, base)
	}
	if err := validateRequestID(filepath.Base(child)); err != nil {
		return "", "", err
	}
	return base, child, nil
}

func marshalCacheOwner(owner cacheOwner) ([]byte, error) {
	data, err := json.Marshal(owner)
	if err != nil {
		return nil, fmt.Errorf("marshal cache owner: %w", err)
	}
	return data, nil
}

func writeCacheOwnerBytesAtomic(dir string, data []byte) (retErr error) {
	tmpPath := filepath.Join(dir, cacheOwnerFileName+".tmp")
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create cache owner temp file: %w", err)
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, os.Remove(tmpPath))
		}
	}()
	if _, err := f.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write cache owner: %w", err), f.Close())
	}
	if err := f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync cache owner: %w", err), f.Close())
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close cache owner: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, cacheOwnerFileName)); err != nil {
		return fmt.Errorf("publish cache owner: %w", err)
	}
	return nil
}

// readCacheOwner validates the receipt in dir against the directory's own name.
func readCacheOwner(dir string) (cacheOwner, bool, error) {
	return readCacheOwnerFor(dir, filepath.Base(dir))
}

// readCacheOwnerFor returns (owner, true, nil) for a valid receipt naming expectedID,
// (_, false, nil) when the directory has no receipt, and an error for a present-but-invalid
// receipt (treated as corrupt by callers, never as permission to delete). Quarantined
// directories are validated against the ID embedded in their reserved name.
func readCacheOwnerFor(dir, expectedID string) (cacheOwner, bool, error) {
	path := filepath.Join(dir, cacheOwnerFileName)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cacheOwner{}, false, nil
		}
		return cacheOwner{}, false, fmt.Errorf("inspect cache owner in %q: %w", dir, err)
	}
	if !info.Mode().IsRegular() {
		return cacheOwner{}, false, fmt.Errorf("cache owner in %q is not a regular file", dir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheOwner{}, false, fmt.Errorf("read cache owner in %q: %w", dir, err)
	}
	var owner cacheOwner
	if err := json.Unmarshal(data, &owner); err != nil {
		return cacheOwner{}, false, fmt.Errorf("parse cache owner in %q: %w", dir, err)
	}
	if owner.PID <= 0 || uint64(owner.PID) > math.MaxUint32 {
		return cacheOwner{}, false, fmt.Errorf("cache owner in %q has invalid pid %d", dir, owner.PID)
	}
	if owner.RequestID != expectedID {
		return cacheOwner{}, false, fmt.Errorf("cache owner in %q names a different request %q", dir, owner.RequestID)
	}
	if runtime.GOOS == "windows" && owner.StartTimeUnixNano <= 0 {
		return cacheOwner{}, false, fmt.Errorf("cache owner in %q lacks a process start time", dir)
	}
	return owner, true, nil
}

// CreateRequestCacheDir creates a fresh, exclusively-owned directory <baseDir>/<requestID>.
// The directory is assembled as creating-<id> with owner.json already published and then
// renamed into place, so it is never visible without ownership. An existing directory of the
// same name is an error: stale ownership is never adopted. If a sweep quarantines the staging
// directory first, the rename fails and the creator reports an error (no data is lost).
func CreateRequestCacheDir(baseDir, requestID string) (string, error) {
	if err := validateRequestID(requestID); err != nil {
		return "", err
	}
	base, exists, err := resolveCacheBase(baseDir)
	if err != nil {
		return "", err
	}
	if !exists {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", fmt.Errorf("create cache base dir: %w", err)
		}
		if _, _, err := resolveCacheBase(base); err != nil {
			return "", err
		}
	}
	startNano, err := processStartUnixNano(os.Getpid())
	if err != nil {
		return "", fmt.Errorf("read own process start time: %w", err)
	}
	finalDir := filepath.Join(base, requestID)
	buildDir := filepath.Join(base, creatingDirPrefix+requestID)
	if err := os.Mkdir(buildDir, 0o755); err != nil {
		return "", fmt.Errorf("create request cache dir: %w", err)
	}
	data, err := marshalCacheOwner(cacheOwner{
		PID:               os.Getpid(),
		StartTimeUnixNano: startNano,
		RequestID:         requestID,
		CreatedUnixNano:   time.Now().UnixNano(),
	})
	if err == nil {
		err = writeCacheOwnerBytesAtomic(buildDir, data)
	}
	if err == nil {
		if _, statErr := os.Lstat(finalDir); statErr == nil {
			err = fmt.Errorf("request cache dir %q already exists", finalDir)
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			err = fmt.Errorf("inspect request cache dir: %w", statErr)
		}
	}
	if err == nil {
		if renameErr := os.Rename(buildDir, finalDir); renameErr != nil {
			err = fmt.Errorf("publish request cache dir: %w", renameErr)
		}
	}
	if err != nil {
		return "", errors.Join(err, removeTreeNoFollow(buildDir))
	}
	return finalDir, nil
}

// removeTreeNoFollow deletes path without ever traversing a reparse point: junctions and
// symlinks are unlinked themselves, never their targets.
func removeTreeNoFollow(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	reparse, err := isReparsePoint(path)
	if err != nil {
		return err
	}
	if reparse || !info.IsDir() {
		return os.Remove(path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		if err := removeTreeNoFollow(filepath.Join(path, entry.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return os.Remove(path)
}

// removeQuarantinedTree removes a directory that already lives in the reserved "removing-"
// namespace. owner.json (matched case-insensitively) is kept until every payload entry is gone,
// so a partial failure (e.g. a locked file) leaves a recoverable receipt for the next sweep; if
// the final rmdir fails after the receipt was removed, the receipt is restored.
func removeQuarantinedTree(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	reparse, err := isReparsePoint(dir)
	if err != nil {
		return err
	}
	if reparse || !info.IsDir() {
		return os.Remove(dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), cacheOwnerFileName) {
			continue
		}
		if err := removeTreeNoFollow(filepath.Join(dir, entry.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	ownerPath := filepath.Join(dir, cacheOwnerFileName)
	saved, readErr := os.ReadFile(ownerPath)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return fmt.Errorf("preserve cache owner before removal: %w", readErr)
	}
	if err := removeTreeNoFollow(ownerPath); err != nil {
		return err
	}
	if err := os.Remove(dir); err != nil {
		if saved != nil {
			err = errors.Join(err, writeCacheOwnerBytesAtomic(dir, saved))
		}
		return err
	}
	return nil
}

// quarantineDir atomically renames base/<name> to base/removing-<nonce>-<id>. The receipt moves
// with the directory, so everything validated afterwards belongs to the moved identity.
// (nil error, "" path) means the directory no longer exists.
func quarantineDir(base, dir, requestID string) (string, error) {
	target := filepath.Join(base, quarantineName(requestID))
	if err := os.Rename(dir, target); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("quarantine %q: %w", dir, err)
	}
	return target, nil
}

// RemoveRequestCacheDir removes dirPath only when it is a direct child of baseDir with a valid
// request ID. It first quarantines the directory with an atomic rename, then deletes it; a
// failure leaves the quarantined directory (with its receipt) for PurgeDeadOwnedCacheDirs.
// A missing directory is not an error; reparse points are unlinked, not followed.
func RemoveRequestCacheDir(baseDir, dirPath string) error {
	base, child, err := resolveDirectChild(baseDir, dirPath)
	if err != nil {
		return err
	}
	quarantined, err := quarantineDir(base, child, filepath.Base(child))
	if err != nil || quarantined == "" {
		return err
	}
	return removeQuarantinedTree(quarantined)
}

// cleanupRequestCacheDir removes a request cache dir under the default base; "" is a no-op.
func cleanupRequestCacheDir(dirPath string) error {
	if dirPath == "" {
		return nil
	}
	return RemoveRequestCacheDir(DefaultCacheBaseDir(), dirPath)
}

func listCacheChildDirs(baseDir string) (string, []string, error) {
	base, exists, err := resolveCacheBase(baseDir)
	if err != nil {
		return "", nil, err
	}
	if !exists {
		return base, nil, nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return "", nil, fmt.Errorf("read cache base dir: %w", err)
	}
	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(base, entry.Name())
		reparse, err := isReparsePoint(path)
		if err != nil || reparse {
			continue
		}
		dirs = append(dirs, path)
	}
	return base, dirs, nil
}

// reapDeadRequestDir quarantines a "req-" dir whose receipt names a dead owner, re-validates the
// receipt of the *moved* directory (so a concurrently republished request is never deleted) and
// then deletes it. If the moved directory turns out to be live, it is moved back.
func reapDeadRequestDir(base, dir string) (bool, error) {
	id := filepath.Base(dir)
	quarantined, err := quarantineDir(base, dir, id)
	if err != nil || quarantined == "" {
		return false, err
	}
	owner, ok, err := readCacheOwnerFor(quarantined, id)
	if err != nil || !ok || cacheOwnerAlive(owner) {
		restoreErr := os.Rename(quarantined, dir)
		if err == nil {
			err = restoreErr
		}
		return false, err
	}
	if err := removeQuarantinedTree(quarantined); err != nil {
		return false, err
	}
	return true, nil
}

// PurgeDeadOwnedCacheDirs removes only "req-" direct-child dirs that carry a valid owner.json
// whose owning process is no longer alive, and finishes interrupted "removing-" quarantines whose
// owner is dead. Unowned or corrupt dirs are never deleted here.
func PurgeDeadOwnedCacheDirs(baseDir string) (int, error) {
	base, dirs, err := listCacheChildDirs(baseDir)
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	for _, dir := range dirs {
		name := filepath.Base(dir)
		switch {
		case hasPrefixFold(name, requestDirPrefix):
			owner, ok, err := readCacheOwner(dir)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !ok || cacheOwnerAlive(owner) {
				continue
			}
			done, err := reapDeadRequestDir(base, dir)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if done {
				removed++
			}
		case hasPrefixFold(name, removingDirPrefix):
			id := requestIDFromReserved(name)
			owner, ok, err := readCacheOwnerFor(dir, id)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if !ok || cacheOwnerAlive(owner) {
				continue
			}
			if err := removeQuarantinedTree(dir); err != nil {
				errs = append(errs, err)
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

// PurgeStaleUnownedCacheDirs removes, by age, only (a) recognized legacy dirs (any name outside the
// reserved "req-"/"creating-"/"removing-" namespaces) that have no owner.json, (b) abandoned
// "creating-" staging dirs, and (c) owner-less leftovers of interrupted removals. Staging dirs are
// quarantined by atomic rename first, so a creator that wins the race publishes normally and one that
// loses gets a clean rename failure; a published request dir is never touched.
func PurgeStaleUnownedCacheDirs(baseDir string, maxAge time.Duration) (int, error) {
	base, dirs, err := listCacheChildDirs(baseDir)
	if err != nil {
		return 0, err
	}
	removed := 0
	var errs []error
	now := time.Now()
	for _, dir := range dirs {
		name := filepath.Base(dir)
		if hasPrefixFold(name, requestDirPrefix) {
			continue
		}
		staging := hasPrefixFold(name, creatingDirPrefix)
		if !staging {
			expectedID := requestIDFromReserved(name)
			if expectedID == "" {
				expectedID = name // recognized legacy dir: any receipt must name the dir itself
			}
			if _, owned, err := readCacheOwnerFor(dir, expectedID); err != nil {
				errs = append(errs, err)
				continue
			} else if owned {
				continue
			}
		}
		info, err := os.Stat(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		target := dir
		if staging {
			quarantined, err := quarantineDir(base, dir, requestIDFromReserved(name))
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if quarantined == "" {
				continue
			}
			target = quarantined
		}
		if err := removeTreeNoFollow(target); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}
