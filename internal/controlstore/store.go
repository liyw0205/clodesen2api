// Package controlstore owns transactional control documents and schema changes.
package controlstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	_ "modernc.org/sqlite"
)

const SchemaVersion = 3

var ErrConflict = errors.New("control document revision conflict")
var ErrMissing = errors.New("control document not found")

type Store struct {
	db   *sql.DB
	path string
}

func Open(path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(absolute); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("control database must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(3000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "synchronous(FULL)")
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	store := &Store{db: db, path: absolute}
	if err := store.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) initialize() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > SchemaVersion {
		return fmt.Errorf("unsupported control schema %d", version)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return err
	}
	if version == SchemaVersion {
		var indexes int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('audit_key_created','audit_type_created','audit_status_created','audit_unfinished','audit_account_created')").Scan(&indexes); err != nil {
			return err
		}
		if indexes == 5 {
			return s.Verify(ctx)
		}
	}
	if version > 0 {
		dir := filepath.Join(filepath.Dir(s.path), "pre-migration")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		file, err := os.CreateTemp(dir, fmt.Sprintf("schema-%d-*.sqlite3", version))
		if err != nil {
			return err
		}
		path := file.Name()
		if err := file.Close(); err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
			return fmt.Errorf("pre-migration backup: %w", err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS documents (name TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision>0), payload BLOB NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS audit_requests (id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, finished INTEGER NOT NULL DEFAULT 0, payload BLOB NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS audit_created ON audit_requests(created_at)`,
		`CREATE TABLE IF NOT EXISTS audit_totals (model TEXT PRIMARY KEY, requests INTEGER NOT NULL, successes INTEGER NOT NULL, failures INTEGER NOT NULL, latency_ms INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS audit_receipts (id TEXT PRIMARY KEY, created_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS audit_receipt_created ON audit_receipts(created_at)`,
		`CREATE INDEX IF NOT EXISTS audit_key_created ON audit_requests(COALESCE(json_extract(CAST(payload AS TEXT),'$.apiKeyId'),''),created_at DESC,id DESC)`,
		`CREATE INDEX IF NOT EXISTS audit_type_created ON audit_requests(COALESCE(json_extract(CAST(payload AS TEXT),'$.type'),''),created_at DESC,id DESC)`,
		`CREATE INDEX IF NOT EXISTS audit_status_created ON audit_requests(` + auditCategory + `,created_at DESC,id DESC)`,
		`CREATE INDEX IF NOT EXISTS audit_unfinished ON audit_requests(created_at) WHERE finished=0`,
		`CREATE INDEX IF NOT EXISTS audit_account_created ON audit_requests(` + auditAccount + `,created_at)`,
		`INSERT OR IGNORE INTO audit_receipts(id,created_at) SELECT id,created_at FROM audit_requests WHERE finished=1`,
		`CREATE TABLE IF NOT EXISTS audit_reports (id TEXT PRIMARY KEY, created_at INTEGER NOT NULL, model TEXT NOT NULL, account TEXT NOT NULL, key_id TEXT NOT NULL, success INTEGER NOT NULL, duration_ms INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS report_created ON audit_reports(created_at)`,
		`CREATE TABLE IF NOT EXISTS report_meta (id INTEGER PRIMARY KEY CHECK(id=1), since INTEGER NOT NULL)`,
		`INSERT OR IGNORE INTO report_meta(id,since) VALUES(1,CAST(strftime('%s','now') AS INTEGER)*1000000000)`,
		`PRAGMA user_version=3`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }

func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) Verify(ctx context.Context) error {
	var integrity string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("control database integrity check failed")
	}
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version != SchemaVersion && version != 2 {
		return fmt.Errorf("unsupported control schema %d", version)
	}
	for _, query := range []string{
		"SELECT name,revision,payload,updated_at FROM documents LIMIT 0",
		"SELECT id,created_at,finished,payload FROM audit_requests LIMIT 0",
		"SELECT model,requests,successes,failures,latency_ms FROM audit_totals LIMIT 0",
		"SELECT id,created_at FROM audit_receipts LIMIT 0",
	} {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("incomplete control schema: %w", err)
		}
		rows.Close()
	}
	if version >= 3 {
		for _, query := range []string{"SELECT id,created_at,model,account,key_id,success,duration_ms FROM audit_reports LIMIT 0", "SELECT id,since FROM report_meta LIMIT 0"} {
			rows, err := s.db.QueryContext(ctx, query)
			if err != nil {
				return fmt.Errorf("incomplete reporting schema: %w", err)
			}
			rows.Close()
		}
	}
	var invalid int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM documents WHERE NOT json_valid(payload)").Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errors.New("invalid control document payload")
	}
	return nil
}

func (s *Store) Load(ctx context.Context, name string, target any) (int64, error) {
	var revision int64
	var payload []byte
	if err := s.db.QueryRowContext(ctx, "SELECT revision,payload FROM documents WHERE name=?", name).Scan(&revision, &payload); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrMissing
	} else if err != nil {
		return 0, err
	}
	return revision, json.Unmarshal(payload, target)
}

func (s *Store) Revision(ctx context.Context, name string) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, "SELECT revision FROM documents WHERE name=?", name).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrMissing
	}
	return revision, err
}

func (s *Store) Save(ctx context.Context, name string, revision int64, value any) (int64, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	var result sql.Result
	if revision == 0 {
		result, err = s.db.ExecContext(ctx, "INSERT INTO documents(name,revision,payload,updated_at) VALUES(?,1,?,?) ON CONFLICT(name) DO NOTHING", name, payload, time.Now().Unix())
	} else {
		result, err = s.db.ExecContext(ctx, "UPDATE documents SET revision=revision+1,payload=?,updated_at=? WHERE name=? AND revision=?", payload, time.Now().Unix(), name, revision)
	}
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count != 1 {
		return 0, ErrConflict
	}
	return revision + 1, nil
}

// Mutate serializes read-modify-write across processes. The callback must be
// quick and must not call back into the store or perform external I/O.
func (s *Store) Mutate(ctx context.Context, name string, change func(json.RawMessage) (any, error)) error {
	return s.MutateWithRead(ctx, name, func(raw json.RawMessage, _ Reader) (any, error) { return change(raw) })
}

// Reader reads another document in the same write transaction. It is only valid
// inside its callback; callers must not retain it or re-enter Store methods.
type Reader func(name string, target any) error

func (s *Store) MutateWithRead(ctx context.Context, name string, change func(json.RawMessage, Reader) (any, error)) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var revision int64
	var raw []byte
	err = conn.QueryRowContext(ctx, "SELECT revision,payload FROM documents WHERE name=?", name).Scan(&revision, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	read := func(name string, target any) error {
		var data []byte
		if err := conn.QueryRowContext(ctx, "SELECT payload FROM documents WHERE name=?", name).Scan(&data); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrMissing
			}
			return err
		}
		return json.Unmarshal(data, target)
	}
	value, err := change(raw, read)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO documents(name,revision,payload,updated_at) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET revision=excluded.revision,payload=excluded.payload,updated_at=excluded.updated_at`, name, revision+1, payload, time.Now().Unix()); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

// Import initializes a document once. Preserve the exact source before import;
// validation is performed by the owning domain before this method is called.
func (s *Store) Import(ctx context.Context, name, source string, value any) error {
	if _, err := s.Revision(ctx, name); err == nil {
		return nil
	} else if !errors.Is(err, ErrMissing) {
		return err
	}
	if err := s.preserveSource(name, source); err != nil {
		return err
	}
	_, err := s.Save(ctx, name, 0, value)
	if errors.Is(err, ErrConflict) {
		return nil
	}
	return err
}

var documentName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,127}$`)

func (s *Store) preserveSource(name, source string) error {
	if !documentName.MatchString(name) {
		return errors.New("invalid import document name")
	}
	if source != "" {
		data, err := os.ReadFile(source)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			dir := filepath.Join(filepath.Dir(s.path), "pre-migration")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			digest := sha256.Sum256(data)
			backupPath := filepath.Join(dir, fmt.Sprintf("%s-%x.json", name, digest))
			backup, err := os.OpenFile(backupPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err == nil {
				_, writeErr := backup.Write(data)
				syncErr := backup.Sync()
				closeErr := backup.Close()
				if writeErr != nil {
					return writeErr
				}
				if syncErr != nil {
					return syncErr
				}
				if closeErr != nil {
					return closeErr
				}
			} else if errors.Is(err, os.ErrExist) {
				info, statErr := os.Lstat(backupPath)
				if statErr != nil || !info.Mode().IsRegular() {
					return errors.New("invalid migration backup")
				}
				existing, readErr := os.ReadFile(backupPath)
				if readErr != nil {
					return readErr
				}
				if !bytes.Equal(existing, data) {
					return errors.New("migration backup checksum mismatch")
				}
			} else {
				return err
			}
		}
	}
	return nil
}

type AuditEvent struct {
	ID           string
	CreatedAt    time.Time
	Payload      any
	Finished     bool
	Count        bool
	Model        string
	Success      bool
	DurationMS   int64
	RecoverOwner string
}

func (s *Store) WriteAudit(ctx context.Context, events []AuditEvent, aggregate any) error {
	return s.write(ctx, func(tx *sql.Conn) error { return writeAudit(ctx, tx, events, aggregate) })
}

// ImportAudit commits the legacy aggregate and its detail rows together. A
// competing startup observes either the entire import or none of it.
func (s *Store) ImportAudit(ctx context.Context, source string, aggregate any, events []AuditEvent) error {
	if err := s.preserveSource("usage", source); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Conn) error {
		var present int
		err := tx.QueryRowContext(ctx, "SELECT 1 FROM documents WHERE name='usage'").Scan(&present)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return writeAudit(ctx, tx, events, aggregate)
	})
}

func (s *Store) write(ctx context.Context, change func(*sql.Conn) error) error {
	tx, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	if _, err := tx.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer tx.ExecContext(context.Background(), "ROLLBACK")
	if err := change(tx); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "COMMIT")
	return err
}

func writeAudit(ctx context.Context, tx *sql.Conn, events []AuditEvent, aggregate any) error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, event := range events {
		// Old replay is rejected even after its bounded deduplication marker expires.
		if event.CreatedAt.Before(cutoff) {
			continue
		}
		if event.RecoverOwner != "" {
			var ownersRaw []byte
			var owners map[string]time.Time
			err := tx.QueryRowContext(ctx, "SELECT payload FROM documents WHERE name='audit-owners'").Scan(&ownersRaw)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if len(ownersRaw) > 0 {
				if err := json.Unmarshal(ownersRaw, &owners); err != nil {
					return err
				}
			}
			if owners[event.RecoverOwner].After(time.Now()) {
				continue
			}
		}
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return err
		}
		var finished bool
		err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM audit_receipts WHERE id=?)", event.ID).Scan(&finished)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if finished {
			continue
		}
		if event.Finished {
			if _, err := tx.ExecContext(ctx, "INSERT INTO audit_receipts(id,created_at) VALUES(?,?)", event.ID, event.CreatedAt.UnixNano()); err != nil {
				return err
			}
		}
		if event.Finished && !finished && event.Count {
			success, failure := 0, 1
			if event.Success {
				success, failure = 1, 0
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO audit_totals(model,requests,successes,failures,latency_ms) VALUES(?,1,?,?,?) ON CONFLICT(model) DO UPDATE SET requests=requests+1,successes=successes+excluded.successes,failures=failures+excluded.failures,latency_ms=latency_ms+excluded.latency_ms`, event.Model, success, failure, event.DurationMS); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_requests(id,created_at,finished,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET finished=excluded.finished,payload=excluded.payload`, event.ID, event.CreatedAt.UnixNano(), event.Finished, payload); err != nil {
			return err
		}
		if event.Finished {
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO audit_reports SELECT id,created_at,COALESCE(json_extract(CAST(payload AS TEXT),'$.model'),''),COALESCE(NULLIF(json_extract(CAST(payload AS TEXT),'$.accountId'),''),json_extract(CAST(payload AS TEXT),'$.account'),''),COALESCE(json_extract(CAST(payload AS TEXT),'$.apiKeyId'),''),COALESCE(json_extract(CAST(payload AS TEXT),'$.success'),0),COALESCE(json_extract(CAST(payload AS TEXT),'$.durationMs'),0) FROM audit_requests WHERE id=?`, event.ID); err != nil {
				return err
			}
		}
	}
	if aggregate != nil {
		payload, err := json.Marshal(aggregate)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO documents(name,revision,payload,updated_at) VALUES('usage',1,?,?) ON CONFLICT(name) DO UPDATE SET revision=revision+1,payload=excluded.payload,updated_at=excluded.updated_at`, payload, time.Now().Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM audit_requests WHERE created_at < ?", time.Now().Add(-7*24*time.Hour).UnixNano()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM audit_receipts WHERE created_at < ?", cutoff.UnixNano()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM audit_reports WHERE created_at < ?", time.Now().Add(-31*24*time.Hour).UnixNano()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE report_meta SET since=MAX(since,?) WHERE id=1", time.Now().Add(-31*24*time.Hour).UnixNano()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM audit_requests WHERE id IN (SELECT id FROM audit_requests ORDER BY created_at DESC LIMIT -1 OFFSET 50000)"); err != nil {
		return err
	}
	return nil
}

type AuditTotal struct {
	Model                                    string
	Requests, Successes, Failures, LatencyMS int64
}

func (s *Store) AuditTotals(ctx context.Context) ([]AuditTotal, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT model,requests,successes,failures,latency_ms FROM audit_totals")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []AuditTotal
	for rows.Next() {
		var item AuditTotal
		if err := rows.Scan(&item.Model, &item.Requests, &item.Successes, &item.Failures, &item.LatencyMS); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) AuditRecords(ctx context.Context) ([]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM audit_requests ORDER BY created_at LIMIT 50000")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []json.RawMessage
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		result = append(result, json.RawMessage(payload))
	}
	return result, rows.Err()
}

// ReadAudit reads aggregates and detail from the same committed database view.
func (s *Store) ReadAudit(ctx context.Context, baseline any) ([]AuditTotal, []json.RawMessage, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT payload FROM documents WHERE name='usage'").Scan(&raw); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(raw, baseline); err != nil {
		return nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT model,requests,successes,failures,latency_ms FROM audit_totals")
	if err != nil {
		return nil, nil, err
	}
	var totals []AuditTotal
	for rows.Next() {
		var item AuditTotal
		if err := rows.Scan(&item.Model, &item.Requests, &item.Successes, &item.Failures, &item.LatencyMS); err != nil {
			rows.Close()
			return nil, nil, err
		}
		totals = append(totals, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, "SELECT payload FROM audit_requests ORDER BY created_at LIMIT 50000")
	if err != nil {
		return nil, nil, err
	}
	var records []json.RawMessage
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, nil, err
		}
		records = append(records, json.RawMessage(raw))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return totals, records, nil
}
