package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadJSONRejectsMultipleValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	if err := os.WriteFile(path, []byte(`{"workload":"short_mix"} {"workload":"long_track"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := readJSON[map[string]string](path); err == nil {
		t.Fatal("readJSON() error = nil, want multiple values error")
	}
}
