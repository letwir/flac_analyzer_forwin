// Package dispatcher provides actors, worker pool coordination, and IO monad execution.
// SideEffectFn: Storage Defense & Garbage Collection
package dispatcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// cleanupCache deletes the temporary cache directory for a given track hash upon task termination.
func cleanupCache(trackHash string) {
	if trackHash == "" {
		return
	}
	cacheDir := filepath.Join(os.TempDir(), "flac_analyzer_cache", trackHash)
	if _, err := os.Stat(cacheDir); err == nil {
		_ = os.RemoveAll(cacheDir)
	}
}

// cleanupQueueFiles removes intermediate JSON files generated for a task if it fails or aborts.
func cleanupQueueFiles(queueDir, trackHash, baseName string) {
	if queueDir == "" || trackHash == "" {
		return
	}
	outName := fmt.Sprintf("%s_%s.json", trackHash, baseName)
	outNameEss := fmt.Sprintf("%s_%s_essentia.json", trackHash, baseName)
	outNameTensor := fmt.Sprintf("%s_%s_tensor.json", trackHash, baseName)

	for _, name := range []string{outName, outNameEss, outNameTensor} {
		p := filepath.Join(queueDir, name)
		if _, err := os.Stat(p); err == nil {
			_ = os.Remove(p)
		}
	}
}

// PurgeOrphanedQueueAndCacheFiles cleans up old cache directories and stale intermediate JSON files.
// SideEffectFn: PurgeOrphanedQueueAndCacheFiles
func PurgeOrphanedQueueAndCacheFiles(queueDir string, maxAge time.Duration) {
	// 1. Purge Temp cache directory
	cacheRoot := filepath.Join(os.TempDir(), "flac_analyzer_cache")
	entries, err := os.ReadDir(cacheRoot)
	if err == nil {
		now := time.Now()
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dirPath := filepath.Join(cacheRoot, entry.Name())
			info, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			if now.Sub(info.ModTime()) > maxAge {
				_ = os.RemoveAll(dirPath)
			}
		}
	}

	// 2. Purge stale queue JSON files
	if queueDir == "" {
		return
	}
	qEntries, qErr := os.ReadDir(queueDir)
	if qErr != nil {
		return
	}
	now := time.Now()
	for _, entry := range qEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		filePath := filepath.Join(queueDir, entry.Name())
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			_ = os.Remove(filePath)
		}
	}
}
