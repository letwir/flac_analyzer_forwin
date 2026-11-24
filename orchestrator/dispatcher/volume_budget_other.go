//go:build !windows

package dispatcher

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func resolveVolumeIdentity(anchor string) (string, error) {
	resolved := anchor
	if eval, err := filepath.EvalSymlinks(anchor); err == nil && eval != "" {
		resolved = eval
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat volume anchor %q: %w", resolved, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return "", fmt.Errorf("missing syscall.Stat_t for %q", resolved)
	}
	return fmt.Sprintf("dev:%d", st.Dev), nil
}
