//go:build !windows

package doctor

import (
	"fmt"
	"syscall"
)

// freeBytes returns the bytes available to an unprivileged user on the
// filesystem holding dir.
func freeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:unconvert // field types differ by OS
}
