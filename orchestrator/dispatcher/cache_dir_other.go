//go:build !windows

package dispatcher

import (
	"fmt"
	"os"
)

func isReparsePoint(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("lstat %q: %w", path, err)
	}
	return info.Mode()&os.ModeSymlink != 0, nil
}

// processStartUnixNano is unsupported off Windows; 0 means "unknown start time".
func processStartUnixNano(int) (int64, error) { return 0, nil }

// processMatchesOwner is conservative off Windows: without a portable liveness probe the
// owner is assumed alive, so no owned directory is ever purged.
func processMatchesOwner(cacheOwner) bool { return true }
