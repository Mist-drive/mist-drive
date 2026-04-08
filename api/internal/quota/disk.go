package quota

import "syscall"

// DiskFree returns the number of bytes available (to a non-root user) on
// the filesystem backing `path`. Returns 0 on error rather than failing —
// callers treat 0 as "unknown, skip the check".
func DiskFree(path string) int64 {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0
	}
	// Bavail = blocks available to unprivileged user.
	return int64(s.Bavail) * int64(s.Bsize)
}
