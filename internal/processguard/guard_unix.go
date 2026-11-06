//go:build !windows

package processguard

import (
	"golang.org/x/sys/unix"
	"os"
)

func lock(file *os.File, exclusive bool) error {
	mode := unix.LOCK_SH
	if exclusive {
		mode = unix.LOCK_EX
	}
	return unix.Flock(int(file.Fd()), mode|unix.LOCK_NB)
}
func unlock(file *os.File) { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN) }
