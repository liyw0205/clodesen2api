//go:build windows

package updater

import "os"

func Restart() error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	process, err := os.StartProcess(executable, os.Args, &os.ProcAttr{Env: os.Environ(), Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}})
	if err != nil {
		return err
	}
	return process.Release()
}
