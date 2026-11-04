//go:build !windows

package updater

import (
	"os"
	"syscall"
)

func Restart() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	arguments := append([]string{executable}, os.Args[1:]...)
	return syscall.Exec(executable, arguments, os.Environ())
}
