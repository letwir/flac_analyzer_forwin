package dispatcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateRequestCacheDirPublishesOwnerAndRejectsUnsafeIDs(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	dir, err := CreateRequestCacheDir(base, "req-1")
	if err != nil {
		t.Fatalf("CreateRequestCacheDir: %v", err)
	}
	if filepath.Dir(dir) != base || filepath.Base(dir) != "req-1" {
		t.Fatalf("expected direct child req-1 of %q, got %q", base, dir)
	}
	owner, ok, err := readCacheOwner(dir)
	if err != nil || !ok {
		t.Fatalf("expected valid owner.json, ok=%v err=%v", ok, err)
	}
	if owner.PID != os.Getpid() || owner.RequestID != "req-1" {
		t.Fatalf("unexpected owner receipt: %+v", owner)
	}
	if _, err := os.Stat(filepath.Join(dir, cacheOwnerFileName+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("temp owner file must not remain after atomic publish, err=%v", err)
	}
	if _, err := CreateRequestCacheDir(base, "req-1"); err == nil {
		t.Fatal("an existing request dir must never be adopted")
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, ".hidden", "x y", strings.Repeat("a", 200)} {
		if _, err := CreateRequestCacheDir(base, bad); err == nil {
			t.Fatalf("expected request ID %q to be rejected", bad)
		}
	}
	if id := NewCacheRequestID(); validateRequestID(id) != nil || id == NewCacheRequestID() {
		t.Fatalf("NewCacheRequestID must be unique and path-safe, got %q", id)
	}
}

func TestRemoveRequestCacheDirEnforcesDirectChildContainment(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "cache")
	dir, err := CreateRequestCacheDir(base, "req-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mix.npy"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	for name, target := range map[string]string{
		"base itself":     base,
		"outside sibling": outside,
		"nested path":     nested,
		"traversal":       filepath.Join(base, "..", "outside"),
		"empty":           "",
	} {
		if err := RemoveRequestCacheDir(base, target); err == nil {
			t.Fatalf("%s: expected containment refusal", name)
		}
	}
	for _, p := range []string{base, outside, nested} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("refused removal must leave %q intact: %v", p, err)
		}
	}

	if err := RemoveRequestCacheDir(base, dir); err != nil {
		t.Fatalf("remove own request dir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected request dir removed, err=%v", err)
	}
	if err := RemoveRequestCacheDir(base, dir); err != nil {
		t.Fatalf("removing a missing dir must be a no-op, got %v", err)
	}
	if err := cleanupRequestCacheDir(""); err != nil {
		t.Fatalf("empty request dir cleanup must be a no-op, got %v", err)
	}
}

func TestPurgeDeadOwnedCacheDirsOnlyRemovesDeadOwners(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	dead, err := CreateRequestCacheDir(base, "req-dead")
	if err != nil {
		t.Fatal(err)
	}
	alive, err := CreateRequestCacheDir(base, "req-alive")
	if err != nil {
		t.Fatal(err)
	}
	unowned := filepath.Join(base, "legacyhash")
	if err := os.Mkdir(unowned, 0o755); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(base, "req-corrupt")
	if err := os.Mkdir(corrupt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corrupt, cacheOwnerFileName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	orig := cacheOwnerAlive
	t.Cleanup(func() { cacheOwnerAlive = orig })
	cacheOwnerAlive = func(owner cacheOwner) bool { return owner.RequestID != "req-dead" }

	removed, err := PurgeDeadOwnedCacheDirs(base)
	if err == nil || !strings.Contains(err.Error(), "parse cache owner") {
		t.Fatalf("expected corrupt owner.json to be reported, got %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected exactly the dead-owner dir removed, removed=%d", removed)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead-owned dir must be removed, err=%v", err)
	}
	for _, keep := range []string{alive, unowned, corrupt, filepath.Join(base, "stray.txt")} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%q must survive dead-owner purge: %v", keep, err)
		}
	}
	if removed, err := PurgeDeadOwnedCacheDirs(filepath.Join(t.TempDir(), "missing")); removed != 0 || err != nil {
		t.Fatalf("missing base must be a no-op, removed=%d err=%v", removed, err)
	}
}

func TestPurgeStaleUnownedCacheDirsSkipsOwnedAndFresh(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	owned, err := CreateRequestCacheDir(base, "req-owned")
	if err != nil {
		t.Fatal(err)
	}
	oldLegacy := filepath.Join(base, "oldlegacy")
	freshLegacy := filepath.Join(base, "freshlegacy")
	for _, d := range []string{oldLegacy, freshLegacy} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-3 * time.Hour)
	for _, d := range []string{oldLegacy, owned} {
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := PurgeStaleUnownedCacheDirs(base, time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("expected only the stale unowned dir removed, removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(oldLegacy); !os.IsNotExist(err) {
		t.Fatalf("stale unowned dir must be removed, err=%v", err)
	}
	for _, keep := range []string{owned, freshLegacy} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("%q must survive stale-unowned purge: %v", keep, err)
		}
	}
}

func TestCacheOwnerJSONRoundTrip(t *testing.T) {
	data, err := json.Marshal(cacheOwner{PID: 7, StartTimeUnixNano: 9, RequestID: "r", CreatedUnixNano: 11})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"pid"`, `"start_time_unix_nano"`, `"request_id"`, `"created_unix_nano"`} {
		if !strings.Contains(string(data), key) {
			t.Fatalf("owner.json schema missing %s: %s", key, data)
		}
	}
}
