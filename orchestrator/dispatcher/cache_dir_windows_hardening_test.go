//go:build windows

package dispatcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func quarantinedDirs(t *testing.T, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if hasPrefixFold(e.Name(), removingDirPrefix) {
			out = append(out, filepath.Join(base, e.Name()))
		}
	}
	return out
}

func TestRemoveRequestCacheDirKeepsReceiptWhenPayloadLockedAndSweepRetries(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	dir, err := CreateRequestCacheDir(base, "req-locked")
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(dir, "mix.npy")
	if err := os.WriteFile(payload, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(payload) // Go opens without FILE_SHARE_DELETE, so deletion must fail.
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveRequestCacheDir(base, dir); err == nil {
		_ = held.Close()
		t.Skip("platform allowed deleting an open file; cannot exercise partial cleanup")
	}
	// Windows refuses to rename a directory that contains an open file, so the quarantine may fail
	// and leave the dir under its public name; if it succeeds the dir lives in removing-*. Either
	// way the receipt must survive and the next dead-owner sweep must finish the job.
	receiptDir := ""
	if _, statErr := os.Stat(dir); statErr == nil {
		receiptDir = dir
	} else if q := quarantinedDirs(t, base); len(q) == 1 {
		receiptDir = q[0]
	}
	if receiptDir == "" {
		_ = held.Close()
		t.Fatal("expected the dir either under its public name or in exactly one quarantine")
	}
	expectedID := "req-locked"
	if _, ok, err := readCacheOwnerFor(receiptDir, expectedID); err != nil || !ok {
		_ = held.Close()
		t.Fatalf("receipt must survive a failed cleanup for retry, ok=%v err=%v", ok, err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}

	orig := cacheOwnerAlive
	t.Cleanup(func() { cacheOwnerAlive = orig })
	cacheOwnerAlive = func(cacheOwner) bool { return false }
	removed, err := PurgeDeadOwnedCacheDirs(base)
	if err != nil || removed != 1 {
		t.Fatalf("dead-owner sweep must finish the interrupted removal, removed=%d err=%v", removed, err)
	}
	if left := quarantinedDirs(t, base); len(left) != 0 {
		t.Fatalf("quarantine must be gone after retry, left=%v", left)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("public dir must be gone after retry, err=%v", statErr)
	}
}

func TestReapDeadRequestDirRestoresDirectoryWhenOwnerIsActuallyLive(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	dir, err := CreateRequestCacheDir(base, "req-live")
	if err != nil {
		t.Fatal(err)
	}
	orig := cacheOwnerAlive
	t.Cleanup(func() { cacheOwnerAlive = orig })
	cacheOwnerAlive = func(cacheOwner) bool { return true } // re-validation after the move says: live

	done, err := reapDeadRequestDir(base, dir)
	if err != nil || done {
		t.Fatalf("a live owner must never be reaped, done=%v err=%v", done, err)
	}
	if _, ok, err := readCacheOwner(dir); err != nil || !ok {
		t.Fatalf("live request dir must be restored under its public name, ok=%v err=%v", ok, err)
	}
}

func TestCaseInsensitiveAliasesAreRespected(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-5 * time.Hour)
	orphan := filepath.Join(base, "REQ-UPPER")
	if err := os.Mkdir(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(orphan, past, past); err != nil {
		t.Fatal(err)
	}
	if removed, err := PurgeStaleUnownedCacheDirs(base, time.Hour); err != nil || removed != 0 {
		t.Fatalf("REQ-* alias must stay in the reserved namespace, removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatalf("ownerless REQ-* dir must survive the age sweep: %v", err)
	}

	dir, err := CreateRequestCacheDir(base, "req-case")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mix.npy"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, cacheOwnerFileName), filepath.Join(dir, "OWNER.JSON")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRequestCacheDir(base, dir); err != nil {
		t.Fatalf("removal with an upper-case receipt name must succeed: %v", err)
	}
}

func TestProcessMatchesOwnerTreatsTerminatedProcessWithHeldHandleAsDead(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "ping -n 30 127.0.0.1 >nul")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	start, err := processStartUnixNano(pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	extra, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	defer windows.CloseHandle(extra) // keeps the terminated process object alive during the check

	owner := cacheOwner{PID: pid, StartTimeUnixNano: start}
	if !processMatchesOwner(owner) {
		t.Fatal("running child must be alive")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for processMatchesOwner(owner) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processMatchesOwner(owner) {
		t.Fatalf("terminated process with a still-open handle must be reported dead (%s)", strings.TrimSpace("zombie"))
	}
}
