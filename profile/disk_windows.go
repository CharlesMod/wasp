//go:build windows

package profile

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// DiskFree is the bytes free for the user where path would live: the nearest
// existing parent is asked.
func DiskFree(path string) uint64 {
	for p := path; ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			ptr, err := syscall.UTF16PtrFromString(p)
			if err != nil {
				return 0
			}
			var free, total, totalFree uint64
			r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW").
				Call(uintptr(unsafe.Pointer(ptr)), uintptr(unsafe.Pointer(&free)),
					uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&totalFree)))
			if r == 0 {
				return 0
			}
			return free
		}
		if filepath.Dir(p) == p {
			return 0
		}
	}
}
