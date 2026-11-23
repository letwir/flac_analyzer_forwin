//go:build windows

package dispatcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Skipf("cannot create junction (%v): %s", err, out)
	}
}

func TestRemoveRequestCacheDirDoesNotFollowJunctions(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "cache")
	dir, err := CreateRequestCacheDir(base, "req-junction")
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(outside, "precious.txt")
	if err := os.WriteFile(precious, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, filepath.Join(dir, "link"), outside)

	reparse, err := isReparsePoint(filepath.Join(dir, "link"))
	if err != nil || !reparse {
		t.Fatalf("expected junction to be detected as reparse point, reparse=%v err=%v", reparse, err)
	}
	if err := RemoveRequestCacheDir(base, dir); err != nil {
		t.Fatalf("remove dir containing junction: %v", err)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("junction target content must never be deleted: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("request dir must be gone, err=%v", err)
	}
}

func TestCleanupSkipsJunctionChildAndRefusesJunctionBase(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	victim := filepath.Join(outside, "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, cacheOwnerFileName), []byte(`{"pid":1,"request_id":"victim"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "cache")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, filepath.Join(base, "req-link"), victim)

	orig := cacheOwnerAlive
	t.Cleanup(func() { cacheOwnerAlive = orig })
	cacheOwnerAlive = func(cacheOwner) bool { return false }
	if removed, err := PurgeDeadOwnedCacheDirs(base); removed != 0 || err != nil {
		t.Fatalf("junction children must be skipped, removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(victim, cacheOwnerFileName)); err != nil {
		t.Fatalf("junction target must be untouched: %v", err)
	}

	linkBase := filepath.Join(root, "linkbase")
	makeJunction(t, linkBase, outside)
	if _, err := CreateRequestCacheDir(linkBase, "req-x"); err == nil {
		t.Fatal("a reparse-point cache base must be refused")
	}
}

func TestProcessMatchesOwnerDetectsLiveDeadAndPIDReuse(t *testing.T) {
	startNano, err := processStartUnixNano(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	self := cacheOwner{PID: os.Getpid(), StartTimeUnixNano: startNano}
	if !processMatchesOwner(self) {
		t.Fatal("own live process with matching start time must be alive")
	}
	reused := cacheOwner{PID: os.Getpid(), StartTimeUnixNano: startNano + 1_000_000_000}
	if processMatchesOwner(reused) {
		t.Fatal("same PID with a different start time must be treated as PID reuse (dead owner)")
	}

	cmd := exec.Command("cmd", "/c", "ping -n 30 127.0.0.1 >nul")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	childStart, err := processStartUnixNano(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	child := cacheOwner{PID: cmd.Process.Pid, StartTimeUnixNano: childStart}
	if !processMatchesOwner(child) {
		t.Fatal("running child must be alive")
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	if processMatchesOwner(child) {
		t.Fatal("exited child must be reported dead")
	}
}
