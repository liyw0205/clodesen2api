// Package backups creates verified archives with explicit restoration scope.
package backups

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"clodesen2api/internal/controlstore"
	"github.com/pelletier/go-toml/v2"
)

const maxArchiveBytes int64 = 512 << 20

type Entry struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Sensitive bool   `json:"sensitive"`
}
type Manifest struct {
	Format    int       `json:"format"`
	Schema    int       `json:"schema"`
	Version   string    `json:"version"`
	Scope     string    `json:"scope"`
	CreatedAt time.Time `json:"createdAt"`
	Entries   []Entry   `json:"entries"`
}
type Record struct {
	ID        string    `json:"id"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
	Automatic bool      `json:"automatic"`
	Warning   string    `json:"warning,omitempty"`
}
type Plan struct {
	ID        string    `json:"id"`
	Manifest  Manifest  `json:"manifest"`
	Validated bool      `json:"validated"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type Service struct {
	control  *controlstore.Store
	dir      string
	files    map[string]string
	version  string
	mu       sync.Mutex
	plans    map[string]Plan
	validate func(context.Context, string) error
}

func (s *Service) SetValidator(validate func(context.Context, string) error) {
	s.mu.Lock()
	s.validate = validate
	s.mu.Unlock()
}

func New(control *controlstore.Store, dir, version string, files map[string]string) (*Service, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	for name := range files {
		if !allowed(name) || name == "control.sqlite3" {
			return nil, errors.New("invalid backup file name")
		}
	}
	copyFiles := map[string]string{}
	for name, path := range files {
		copyFiles[name] = path
	}
	// Expired uploads are not useful after a process restart.
	if !Pending(dir) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "restore-") && entry.IsDir() {
				if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > 15*time.Minute {
					_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
				}
			}
		}
	}
	return &Service{control: control, dir: dir, files: copyFiles, version: version, plans: map[string]Plan{}}, nil
}
func randomID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func allowed(name string) bool {
	return name == "control.sqlite3" || name == "config.toml" || name == "admin.token" || name == "credit-usage.json" || name == "redemption-history.json" || name == "admin-cache.json"
}
func (s *Service) Create(ctx context.Context, automatic bool) (Record, error) {
	keep := 7
	if automatic {
		if view, err := s.Schedule(ctx); err == nil {
			keep = view.Settings.Retain
		}
	}
	return s.create(ctx, automatic, keep)
}
func (s *Service) create(ctx context.Context, automatic bool, keep int) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := randomID()
	if err != nil {
		return Record{}, err
	}
	prefix := "manual-"
	if automatic {
		prefix = "auto-"
	}
	id = prefix + id
	stage, err := os.MkdirTemp(s.dir, ".backup-")
	if err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(stage)
	dbPath := filepath.Join(stage, "control.sqlite3")
	if err := s.control.Backup(ctx, dbPath); err != nil {
		return Record{}, err
	}
	files := map[string]string{"control.sqlite3": dbPath}
	for name, source := range s.files {
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Record{}, err
		}
		if !info.Mode().IsRegular() {
			return Record{}, errors.New("backup source must be regular file")
		}
		files[name] = source
	}
	manifest := Manifest{Format: 1, Schema: controlstore.SchemaVersion, Version: s.version, Scope: "full", CreatedAt: time.Now().UTC()}
	path := filepath.Join(s.dir, id+".zip")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Record{}, err
	}
	ok := false
	defer func() {
		file.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	archive := zip.NewWriter(file)
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var total int64
	for _, name := range names {
		input, err := os.Open(files[name])
		if err != nil {
			return Record{}, err
		}
		info, err := input.Stat()
		if err != nil {
			input.Close()
			return Record{}, err
		}
		total += info.Size()
		if total > maxArchiveBytes {
			input.Close()
			return Record{}, errors.New("backup exceeds size limit")
		}
		writer, err := archive.Create(name)
		if err != nil {
			input.Close()
			return Record{}, err
		}
		digest := sha256.New()
		n, err := io.Copy(io.MultiWriter(writer, digest), io.LimitReader(input, maxArchiveBytes+1))
		input.Close()
		if err != nil {
			return Record{}, err
		}
		if n != info.Size() {
			return Record{}, errors.New("backup source changed size")
		}
		manifest.Entries = append(manifest.Entries, Entry{Name: name, Size: n, SHA256: hex.EncodeToString(digest.Sum(nil)), Sensitive: true})
	}
	writer, err := archive.Create("manifest.json")
	if err != nil {
		return Record{}, err
	}
	if err := json.NewEncoder(writer).Encode(manifest); err != nil {
		return Record{}, err
	}
	if err := archive.Close(); err != nil {
		return Record{}, err
	}
	if err := file.Sync(); err != nil {
		return Record{}, err
	}
	if err := file.Close(); err != nil {
		return Record{}, err
	}
	if _, err := Validate(ctx, path, stage+"-verify"); err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(stage + "-verify")
	info, err := os.Stat(path)
	if err != nil {
		return Record{}, err
	}
	ok = true
	warning := ""
	if automatic {
		if err := s.pruneAutomatic(keep); err != nil {
			warning = "cleanup_failed"
		}
	}
	return Record{ID: id, Size: info.Size(), CreatedAt: manifest.CreatedAt, Automatic: automatic, Warning: warning}, nil
}
func validID(id string) bool {
	if len(id) != 39 && len(id) != 37 {
		return false
	}
	prefix, hexID, ok := strings.Cut(id, "-")
	if !ok || (prefix != "manual" && prefix != "auto") {
		return false
	}
	b, err := hex.DecodeString(hexID)
	return err == nil && len(b) == 16
}
func (s *Service) Path(id string) (string, error) {
	if !validID(id) {
		return "", errors.New("invalid backup ID")
	}
	return filepath.Join(s.dir, id+".zip"), nil
}
func (s *Service) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.Path(id)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
func (s *Service) List() ([]Record, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	records := []Record{}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".zip")
		if !strings.HasSuffix(entry.Name(), ".zip") || !validID(id) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		records = append(records, Record{ID: id, Size: info.Size(), CreatedAt: info.ModTime(), Automatic: strings.HasPrefix(id, "auto-")})
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	return records, nil
}
func (s *Service) pruneAutomatic(keep int) error {
	records, err := s.List()
	if err != nil {
		return err
	}
	for _, record := range records {
		if !record.Automatic {
			continue
		}
		keep--
		if keep < 0 {
			path, err := s.Path(record.ID)
			if err != nil {
				return err
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// Validate extracts only named regular files, enforcing both compressed and expanded limits.
func Validate(ctx context.Context, path, dir string) (Manifest, error) {
	var manifest Manifest
	info, err := os.Stat(path)
	if err != nil {
		return manifest, err
	}
	if info.Size() > maxArchiveBytes {
		return manifest, errors.New("archive too large")
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return manifest, err
	}
	defer archive.Close()
	if len(archive.File) < 2 || len(archive.File) > 8 {
		return manifest, errors.New("invalid archive file count")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return manifest, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	seen := map[string]bool{}
	digests := map[string]Entry{}
	var total int64
	for _, entry := range archive.File {
		if seen[entry.Name] || (!allowed(entry.Name) && entry.Name != "manifest.json") || !entry.Mode().IsRegular() || entry.UncompressedSize64 > uint64(maxArchiveBytes) {
			return manifest, errors.New("invalid archive entry")
		}
		seen[entry.Name] = true
		input, err := entry.Open()
		if err != nil {
			return manifest, err
		}
		output, err := os.OpenFile(filepath.Join(dir, entry.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			input.Close()
			return manifest, err
		}
		digest := sha256.New()
		limit := maxArchiveBytes - total
		if entry.Name == "manifest.json" && limit > 64<<10 {
			limit = 64 << 10
		}
		n, err := io.Copy(io.MultiWriter(output, digest), io.LimitReader(input, limit+1))
		input.Close()
		syncErr := output.Sync()
		closeErr := output.Close()
		if err != nil {
			return manifest, err
		}
		if syncErr != nil {
			return manifest, syncErr
		}
		if closeErr != nil {
			return manifest, closeErr
		}
		if n > limit || uint64(n) != entry.UncompressedSize64 {
			return manifest, errors.New("archive expanded size exceeded")
		}
		total += n
		digests[entry.Name] = Entry{Name: entry.Name, Size: n, SHA256: hex.EncodeToString(digest.Sum(nil))}
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	if manifest.Format != 1 || (manifest.Schema != controlstore.SchemaVersion && manifest.Schema != 2) || manifest.Scope != "full" || len(manifest.Entries) != len(archive.File)-1 {
		return manifest, errors.New("unsupported or incomplete manifest")
	}
	listed := map[string]bool{}
	for _, entry := range manifest.Entries {
		actual, ok := digests[entry.Name]
		if !allowed(entry.Name) || !ok || listed[entry.Name] || actual.Size != entry.Size || actual.SHA256 != entry.SHA256 {
			return manifest, errors.New("manifest checksum mismatch")
		}
		listed[entry.Name] = true
	}
	if !listed["control.sqlite3"] {
		return manifest, errors.New("missing control database")
	}
	if listed["config.toml"] {
		raw, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil {
			return manifest, err
		}
		var parsed map[string]any
		if err := toml.Unmarshal(raw, &parsed); err != nil {
			return manifest, errors.New("invalid backup configuration")
		}
	}
	for _, name := range []string{"credit-usage.json", "redemption-history.json", "admin-cache.json"} {
		if listed[name] {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil || !json.Valid(raw) {
				return manifest, errors.New("invalid backup JSON")
			}
		}
	}
	if err := controlstore.VerifyFile(ctx, filepath.Join(dir, "control.sqlite3")); err != nil {
		return manifest, fmt.Errorf("invalid restored database: %w", err)
	}
	ok = true
	return manifest, nil
}

func (s *Service) Prepare(ctx context.Context, input io.Reader) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, plan := range s.plans {
		if !plan.ExpiresAt.After(time.Now()) {
			os.RemoveAll(filepath.Join(s.dir, "restore-"+id))
			delete(s.plans, id)
		}
	}
	if len(s.plans) >= 2 {
		return Plan{}, errors.New("restore staging capacity exceeded")
	}
	id, err := randomID()
	if err != nil {
		return Plan{}, err
	}
	path := filepath.Join(s.dir, ".upload-"+id+".zip")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return Plan{}, err
	}
	defer os.Remove(path)
	n, err := io.Copy(file, io.LimitReader(input, maxArchiveBytes+1))
	closeErr := file.Close()
	if err != nil {
		return Plan{}, err
	}
	if closeErr != nil {
		return Plan{}, closeErr
	}
	if n > maxArchiveBytes {
		return Plan{}, errors.New("archive too large")
	}
	manifest, err := Validate(ctx, path, filepath.Join(s.dir, "restore-"+id))
	if err != nil {
		return Plan{}, err
	}
	if s.validate != nil {
		if err := s.validate(ctx, filepath.Join(s.dir, "restore-"+id)); err != nil {
			os.RemoveAll(filepath.Join(s.dir, "restore-"+id))
			return Plan{}, err
		}
	}
	plan := Plan{ID: id, Manifest: manifest, Validated: true, ExpiresAt: time.Now().Add(15 * time.Minute)}
	s.plans[id] = plan
	return plan, nil
}
