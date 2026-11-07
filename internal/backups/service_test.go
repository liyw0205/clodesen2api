package backups

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"clodesen2api/internal/controlstore"
)

func TestRestoreRollsBackAllReplacedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "control.sqlite3")
	db, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Save(ctx, "sample", 0, "backup"); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte("[server]\naddr=\"backup:1\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "backups")
	s, err := New(db, dir, "test", map[string]string{"config.toml": configPath})
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.Create(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	archive, _ := s.Path(record.ID)
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.Prepare(ctx, file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Save(ctx, "sample", 1, "current"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.WriteFile(configPath, []byte("[server]\naddr=\"current:2\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Arm(plan.ID); err != nil {
		t.Fatal(err)
	}
	replacedConfig := false
	replace := func(from, to string) error {
		if to == path {
			return errors.New("injected database rename failure")
		}
		replacedConfig = true
		return copyFile(from, to)
	}
	if ok, err := applyPending(ctx, dir, path, map[string]string{"config.toml": configPath}, replace); err == nil || ok {
		t.Fatal("failed restore reported success")
	}
	if !replacedConfig {
		t.Fatal("failure happened before first replacement")
	}
	raw, _ := os.ReadFile(configPath)
	if string(raw) != "[server]\naddr=\"current:2\"\n" {
		t.Fatalf("configuration not rolled back: %s", raw)
	}
	db, err = controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value string
	if _, err := db.Load(ctx, "sample", &value); err != nil || value != "current" {
		t.Fatalf("database not rolled back: %s %v", value, err)
	}
	if Pending(dir) {
		t.Fatal("successful rollback left armed restore")
	}
}

func TestWALBackupValidateAndRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "control.sqlite3")
	db, err := controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Save(ctx, "sample", 0, map[string]string{"value": "original"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "backups")
	s, err := New(db, dir, "test-version", nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := s.Create(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	archive, _ := s.Path(record.ID)
	input, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.Prepare(ctx, input)
	input.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Validated || plan.Manifest.Schema != controlstore.SchemaVersion {
		t.Fatal("plan not validated")
	}
	if _, err := db.Save(ctx, "sample", 1, map[string]string{"value": "changed"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Arm(plan.ID); err != nil {
		t.Fatal(err)
	}
	if restored, err := ApplyPending(ctx, dir, path, nil); err != nil || !restored {
		t.Fatalf("restore: %v %v", restored, err)
	}
	db, err = controlstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var value map[string]string
	if _, err := db.Load(ctx, "sample", &value); err != nil {
		t.Fatal(err)
	}
	if value["value"] != "original" {
		t.Fatalf("restored %v", value)
	}
	if restored, err := ApplyPending(ctx, dir, path, nil); err != nil || restored {
		t.Fatalf("replayed completed restore: %v %v", restored, err)
	}
}
func TestRejectsArchiveTraversal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	w, _ := archive.Create("../escape")
	w.Write([]byte("bad"))
	w, _ = archive.Create("manifest.json")
	w.Write([]byte(`{}`))
	archive.Close()
	file.Close()
	if _, err := Validate(context.Background(), path, filepath.Join(dir, "extract")); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape")); !os.IsNotExist(err) {
		t.Fatal("wrote traversal path")
	}
}

func TestDeleteBackup(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := controlstore.Open(filepath.Join(root, "control.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := New(db, filepath.Join(root, "backups"), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(record.ID); err != nil {
		t.Fatalf("delete backup: %v", err)
	}
	if records, err := store.List(); err != nil || len(records) != 0 {
		t.Fatalf("deleted backup still listed: %v %+v", err, records)
	}
	if err := store.Delete(record.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second delete error = %v, want not exist", err)
	}
	if err := store.Delete("not-a-backup"); err == nil {
		t.Fatal("invalid backup ID accepted")
	}
}
