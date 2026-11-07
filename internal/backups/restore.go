package backups

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"clodesen2api/internal/controlstore"
)

// Arm records the reviewed plan. Actual replacement runs before database startup.
func (s *Service) Arm(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.plans[id]
	if !ok || !plan.ExpiresAt.After(time.Now()) {
		return errors.New("restore plan expired")
	}
	manifest, err := json.Marshal(plan.Manifest)
	if err != nil {
		return err
	}
	stage := filepath.Join(s.dir, "restore-"+id)
	if err := writeFile(filepath.Join(stage, "verified-manifest.json"), manifest); err != nil {
		return err
	}
	return writeFile(filepath.Join(s.dir, "pending-restore.json"), []byte(`{"id":"`+id+`"}`))
}

func Pending(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "pending-restore.json"))
	return err == nil
}

func (s *Service) Status() (json.RawMessage, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "restore-status.json"))
	if errors.Is(err, os.ErrNotExist) {
		return json.RawMessage(`{"state":"idle"}`), nil
	}
	return data, err
}

func (s *Service) Disarm() error {
	err := os.Remove(filepath.Join(s.dir, "pending-restore.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func writeFile(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
func copyFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.CreateTemp(filepath.Dir(target), ".restore-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	if err := output.Chmod(0o600); err != nil {
		output.Close()
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	return os.Rename(output.Name(), target)
}

// ApplyPending is called with no open application database or workers. Each
// replacement is atomic; an interrupted multi-file restore replays the same plan.
func ApplyPending(ctx context.Context, dir, dbPath string, files map[string]string, validators ...func(context.Context, string) error) (bool, error) {
	return applyPending(ctx, dir, dbPath, files, copyFile, validators...)
}

func applyPending(ctx context.Context, dir, dbPath string, files map[string]string, replace func(string, string) error, validators ...func(context.Context, string) error) (restored bool, restoreErr error) {
	marker := filepath.Join(dir, "pending-restore.json")
	data, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var pending struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &pending); err != nil {
		return false, err
	}
	decoded, err := hex.DecodeString(pending.ID)
	if err != nil || len(decoded) != 16 {
		return false, errors.New("invalid restore ID")
	}
	stage := filepath.Join(dir, "restore-"+pending.ID)
	raw, err := os.ReadFile(filepath.Join(stage, "verified-manifest.json"))
	if err != nil {
		return false, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return false, err
	}
	if manifest.Format != 1 || (manifest.Schema != controlstore.SchemaVersion && manifest.Schema != 2) || manifest.Scope != "full" {
		return false, errors.New("invalid restore manifest")
	}
	targets := map[string]string{"control.sqlite3": dbPath}
	for name, path := range files {
		if !allowed(name) || name == "control.sqlite3" {
			return false, errors.New("invalid restore target")
		}
		targets[name] = path
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Entries {
		if !allowed(entry.Name) || targets[entry.Name] == "" || seen[entry.Name] || entry.Size < 0 || entry.Size > maxArchiveBytes {
			return false, errors.New("invalid restore entry")
		}
		seen[entry.Name] = true
		path := filepath.Join(stage, entry.Name)
		info, err := os.Lstat(path)
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() || info.Size() != entry.Size {
			return false, errors.New("staged file changed")
		}
		file, err := os.Open(path)
		if err != nil {
			return false, err
		}
		digest := sha256.New()
		_, err = io.Copy(digest, file)
		file.Close()
		if err != nil {
			return false, err
		}
		if hex.EncodeToString(digest.Sum(nil)) != entry.SHA256 {
			return false, errors.New("staged file checksum mismatch")
		}
	}
	if !seen["control.sqlite3"] {
		return false, errors.New("restore database missing")
	}
	if err := controlstore.VerifyFile(ctx, filepath.Join(stage, "control.sqlite3")); err != nil {
		return false, err
	}
	for _, validate := range validators {
		if err := validate(ctx, stage); err != nil {
			return false, err
		}
	}
	// Keep exact originals, including WAL sidecars, before the first replacement.
	original := filepath.Join(stage, "original")
	if err := os.MkdirAll(original, 0o700); err != nil {
		return false, err
	}
	ready := filepath.Join(original, "ready")
	if _, err := os.Stat(ready); errors.Is(err, os.ErrNotExist) {
		for name, target := range targets {
			if _, err := os.Lstat(target); err == nil {
				info, err := os.Lstat(target)
				if err != nil || !info.Mode().IsRegular() {
					return false, errors.New("restore target must be regular")
				}
				if err := copyFile(target, filepath.Join(original, name)); err != nil {
					return false, err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			source := dbPath + suffix
			if _, err := os.Stat(source); err == nil {
				if err := copyFile(source, filepath.Join(original, "control.sqlite3"+suffix)); err != nil {
					return false, err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
		}
		if err := writeFile(ready, []byte("ready")); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	defer func() {
		if restoreErr == nil {
			return
		}
		var rollbackErr error
		for name, target := range targets {
			source := filepath.Join(original, name)
			if _, err := os.Stat(source); err == nil {
				rollbackErr = errors.Join(rollbackErr, copyFile(source, target))
			} else if errors.Is(err, os.ErrNotExist) {
				if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
					rollbackErr = errors.Join(rollbackErr, err)
				}
			} else {
				rollbackErr = errors.Join(rollbackErr, err)
			}
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			source := filepath.Join(original, "control.sqlite3"+suffix)
			if _, err := os.Stat(source); err == nil {
				rollbackErr = errors.Join(rollbackErr, copyFile(source, dbPath+suffix))
			} else if errors.Is(err, os.ErrNotExist) {
				if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
					rollbackErr = errors.Join(rollbackErr, err)
				}
			} else {
				rollbackErr = errors.Join(rollbackErr, err)
			}
		}
		if rollbackErr == nil {
			status, _ := json.Marshal(map[string]any{"id": pending.ID, "state": "rolled-back", "verifiedAt": time.Now().UTC()})
			rollbackErr = errors.Join(writeFile(filepath.Join(dir, "restore-status.json"), status), os.Remove(marker))
		}
		restoreErr = errors.Join(restoreErr, rollbackErr)
	}()
	for _, entry := range manifest.Entries {
		if entry.Name == "control.sqlite3" {
			continue
		}
		if err := replace(filepath.Join(stage, entry.Name), targets[entry.Name]); err != nil {
			return false, err
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if err := replace(filepath.Join(stage, "control.sqlite3"), dbPath); err != nil {
		return false, err
	}
	if err := controlstore.VerifyFile(ctx, dbPath); err != nil {
		return false, err
	}
	status, _ := json.Marshal(map[string]any{"id": pending.ID, "state": "restored", "verifiedAt": time.Now().UTC(), "version": manifest.Version, "originalDirectory": original})
	if err := writeFile(filepath.Join(dir, "restore-status.json"), status); err != nil {
		return false, err
	}
	if err := os.Remove(marker); err != nil {
		return false, err
	}
	return true, nil
}
