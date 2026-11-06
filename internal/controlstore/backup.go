package controlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

func fileURI(path, mode string) string {
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := url.Values{}
	query.Set("mode", mode)
	uri.RawQuery = query.Encode()
	return uri.String()
}

func InspectFile(ctx context.Context, path string, visit func(string, json.RawMessage) error) error {
	if err := VerifyFile(ctx, path); err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", fileURI(abs, "ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT name,payload FROM documents")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return err
		}
		if err := visit(name, raw); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Backup writes a consistent standalone SQLite file including committed WAL data.
func (s *Store) Backup(ctx context.Context, target string) error {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if absolute == s.path {
		return errors.New("backup target is the active database")
	}
	if _, err := os.Lstat(absolute); err == nil {
		return errors.New("backup target exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", absolute); err != nil {
		return err
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		return err
	}
	return VerifyFile(ctx, absolute)
}

// VerifyFile opens read-only, so verification cannot migrate or repair the input.
func VerifyFile(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("backup must be a regular file")
	}
	uri, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", fileURI(uri, "ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	return (&Store{db: db, path: uri}).Verify(ctx)
}
