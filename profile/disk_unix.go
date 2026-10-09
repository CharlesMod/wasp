//go:build linux || darwin

package profile

import (
	"os"
	"path/filepath"
	"syscall"
)

// DiskFree is the bytes free for an ordinary user where path would live: the
// nearest existing parent is asked.
func DiskFree(path string) uint64 {
	for p := path; ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			var st syscall.Statfs_t
			if syscall.Statfs(p, &st) != nil {
				return 0
			}
			return uint64(st.Bavail) * uint64(st.Bsize)
		}
		if filepath.Dir(p) == p {
			return 0
		}
	}
}
