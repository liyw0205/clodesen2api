// Package processguard prevents restoring a database used by another instance.
package processguard

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type Guard struct {
	file, gate *os.File
	mu         sync.Mutex
}

func Open(path string) (*Guard, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	gate, err := os.OpenFile(path+".gate", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		file.Close()
		return nil, err
	}
	g := &Guard{file: file, gate: gate}
	if err := g.Shared(); err != nil {
		g.Close()
		return nil, err
	}
	return g, nil
}

func (g *Guard) Close() error { return errors.Join(g.file.Close(), g.gate.Close()) }

func (g *Guard) Shared() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := lock(g.gate, false); err != nil {
		return err
	}
	defer unlock(g.gate)
	return lock(g.file, false)
}

func (g *Guard) Exclusive() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	// Serialize conversions: a failed flock upgrade may release its shared lock.
	// Holding the gate prevents any competing upgrade until it is restored.
	if err := lock(g.gate, true); err != nil {
		return err
	}
	defer unlock(g.gate)
	if err := lock(g.file, true); err != nil {
		if sharedErr := lock(g.file, false); sharedErr != nil {
			return errors.Join(err, sharedErr)
		}
		return err
	}
	return nil
}
