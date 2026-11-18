//go:build unix

package app

import "syscall"

// freeDiskSpace reports the bytes available to unprivileged writers on the
// filesystem holding dir.
func freeDiskSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}
