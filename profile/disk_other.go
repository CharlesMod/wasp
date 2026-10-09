//go:build !linux && !darwin && !windows

package profile

// DiskFree is unmeasured on this platform.
func DiskFree(string) uint64 { return 0 }
