//go:build windows

package doctor

import (
	"fmt"
	"syscall"
	"unsafe"
)

var procGetDiskFreeSpaceExW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")

// freeBytes returns the bytes available to the calling user on the volume
// holding dir (GetDiskFreeSpaceExW, lpFreeBytesAvailableToCaller).
func freeBytes(dir string) (uint64, error) {
	p, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return 0, fmt.Errorf("diskfree %s: %w", dir, err)
	}
	var avail uint64
	r, _, callErr := procGetDiskFreeSpaceExW.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&avail)), 0, 0)
	if r == 0 {
		return 0, fmt.Errorf("GetDiskFreeSpaceExW %s: %w", dir, callErr)
	}
	return avail, nil
}
