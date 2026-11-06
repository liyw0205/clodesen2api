//go:build windows

package processguard

import (
	"golang.org/x/sys/windows"
	"os"
)

func lock(file *os.File, exclusive bool) error {
	handle := windows.Handle(file.Fd())
	_ = windows.UnlockFileEx(handle, 0, 1, 0, &windows.Overlapped{})
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	return windows.LockFileEx(handle, flags, 0, 1, 0, &windows.Overlapped{})
}
func unlock(file *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &windows.Overlapped{})
}
