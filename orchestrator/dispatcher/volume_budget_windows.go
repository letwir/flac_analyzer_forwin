//go:build windows

package dispatcher

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func resolveVolumeIdentity(anchor string) (string, error) {
	resolved := anchor
	if eval, err := filepath.EvalSymlinks(anchor); err == nil && eval != "" {
		resolved = eval
	}
	ptr, err := windows.UTF16PtrFromString(resolved)
	if err != nil {
		return "", fmt.Errorf("encode volume path %q: %w", resolved, err)
	}
	mountBuf := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(ptr, &mountBuf[0], uint32(len(mountBuf))); err != nil {
		return "", fmt.Errorf("GetVolumePathName(%q): %w", resolved, err)
	}
	volBuf := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumeNameForVolumeMountPoint(&mountBuf[0], &volBuf[0], uint32(len(volBuf))); err != nil {
		return "", fmt.Errorf("GetVolumeNameForVolumeMountPoint(%q): %w", windows.UTF16ToString(mountBuf), err)
	}
	volGUID := strings.TrimSpace(windows.UTF16ToString(volBuf))
	if volGUID == "" {
		return "", fmt.Errorf("empty canonical volume GUID for %q", resolved)
	}
	return strings.ToUpper(volGUID), nil
}
