package dispatcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRequestIDRejectsWindowsAliasesAndReservedNames(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	for _, bad := range []string{"req-", "req-a.", "req-a ", "req-a.txt", "CON", "NUL.txt", "req-CON.", "x-req-1", "creating-req-1"} {
		if _, err := CreateRequestCacheDir(base, bad); err == nil {
			t.Fatalf("expected request ID %q to be rejected", bad)
		}
	}
}

func TestCreateRequestCacheDirNeverExposesOwnerlessDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	dir, err := CreateRequestCacheDir(base, "req-pub")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), creatingDirPrefix) {
			t.Fatalf("build dir must be renamed away after publication, found %q", e.Name())
		}
	}
	if _, ok, err := readCacheOwner(dir); err != nil || !ok {
		t.Fatalf("published dir must already carry owner.json, ok=%v err=%v", ok, err)
	}
}

func TestReadCacheOwnerRejectsInvalidReceipts(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		dir := filepath.Join(base, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, cacheOwnerFileName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	cases := map[string]string{
		"req-wrongid": `{"pid":10,"start_time_unix_nano":5,"request_id":"req-other"}`,
		"req-hugepid": `{"pid":99999999999,"start_time_unix_nano":5,"request_id":"req-hugepid"}`,
		"req-zeropid": `{"pid":0,"start_time_unix_nano":5,"request_id":"req-zeropid"}`,
	}
	for name, body := range cases {
		dir := write(name, body)
		if _, ok, err := readCacheOwner(dir); err == nil || ok {
			t.Fatalf("%s: expected invalid receipt error, ok=%v err=%v", name, ok, err)
		}
	}
	dirReceipt := filepath.Join(base, "req-dirreceipt")
	if err := os.MkdirAll(filepath.Join(dirReceipt, cacheOwnerFileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := readCacheOwner(dirReceipt); err == nil || ok {
		t.Fatalf("a non-regular owner.json must be corrupt, ok=%v err=%v", ok, err)
	}

	orig := cacheOwnerAlive
	t.Cleanup(func() { cacheOwnerAlive = orig })
	cacheOwnerAlive = func(cacheOwner) bool { return false }
	removed, err := PurgeDeadOwnedCacheDirs(base)
	if removed != 0 || err == nil {
		t.Fatalf("invalid receipts must be reported and never authorize deletion, removed=%d err=%v", removed, err)
	}
	for name := range cases {
		if _, statErr := os.Stat(filepath.Join(base, name)); statErr != nil {
			t.Fatalf("%s must survive: %v", name, statErr)
		}
	}
}

func TestStaleSweepRespectsReservedNamespaces(t *testing.T) {
	base := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-5 * time.Hour)
	mk := func(name string) string {
		d := filepath.Join(base, name)
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
		return d
	}
	orphanReq := mk("req-orphan")
	staleBuild := mk("creating-req-abandoned")
	legacy := mk("0123456789abcdef")
	freshBuild := filepath.Join(base, "creating-req-fresh")
	if err := os.Mkdir(freshBuild, 0o755); err != nil {
		t.Fatal(err)
	}

	removed, err := PurgeStaleUnownedCacheDirs(base, time.Hour)
	if err != nil || removed != 2 {
		t.Fatalf("expected stale build dir and legacy dir removed, removed=%d err=%v", removed, err)
	}
	for _, gone := range []string{staleBuild, legacy} {
		if _, statErr := os.Stat(gone); !os.IsNotExist(statErr) {
			t.Fatalf("%q must be removed, err=%v", gone, statErr)
		}
	}
	for _, keep := range []string{orphanReq, freshBuild} {
		if _, statErr := os.Stat(keep); statErr != nil {
			t.Fatalf("%q must survive (req- namespace is never age-swept; fresh build dir is young): %v", keep, statErr)
		}
	}
}

func TestPurgeFunctionsRejectEmptyBase(t *testing.T) {
	if _, err := PurgeDeadOwnedCacheDirs(""); err == nil {
		t.Fatal("empty base must be rejected, never resolved to the working directory")
	}
	if _, err := PurgeStaleUnownedCacheDirs("  ", time.Hour); err == nil {
		t.Fatal("blank base must be rejected")
	}
	if err := RemoveRequestCacheDir("", "req-x"); err == nil {
		t.Fatal("empty base must be rejected for removal")
	}
}
