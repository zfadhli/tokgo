//go:build unix

package boot

import "syscall"

// FreeSpace reports the bytes an unprivileged writer can still put in dir.
func FreeSpace(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(st.Bavail) * int64(st.Bsize), true
}
