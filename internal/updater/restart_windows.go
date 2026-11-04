//go:build windows

package updater

import "errors"

func Restart() error {
	return errors.New("automatic restart is not supported on Windows")
}
