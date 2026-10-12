// Package store wraps the SQLite database: connection setup, migrations,
// and all queries used by the web server.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver, no CGO needed

	"github.com/tylergoza/church-calendar/internal/secrets"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a single-row lookup finds nothing.
var ErrNotFound = errors.New("not found")

type Store struct {
	DB *sql.DB
	// box seals secret settings with the key in secrets.key, which sits
	// next to the database.
	box *secrets.Box
}

// Open opens (creating if needed) the SQLite database at path and applies
// any pending migrations.
func Open(path string) (*Store, error) { return openAt(path, 0) }

// openAt opens the database migrated up to version upTo (0 for all).
func openAt(path string, upTo int) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite allows one writer at a time; a single connection avoids
	// SQLITE_BUSY churn and is plenty for this workload.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	box, err := secrets.LoadOrCreate(filepath.Join(filepath.Dir(path), secrets.KeyFile))
	if err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db, box: box}
	if err := s.migrate(upTo); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// noForeignKeys marks a migration that rebuilds tables other tables point
// at. SQLite needs foreign keys off for that (and can only switch them off
// outside a transaction); they're checked before the migration commits.
const noForeignKeys = "-- foreign_keys: off"

// migrate applies pending migrations up to version upTo (0 for all).
func (s *Store) migrate(upTo int) error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("bad migration name %q", e.Name())
		}
		if upTo > 0 && version > upTo {
			break
		}
		var exists int
		if err := s.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if err := s.runMigration(version, string(body)); err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
	}
	return nil
}

func (s *Store) runMigration(version int, body string) error {
	ctx := context.Background()
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	fkOff := strings.HasPrefix(body, noForeignKeys)
	if fkOff {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return err
		}
		defer conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(body); err != nil {
		return err
	}
	if fkOff {
		var table string
		var parent sql.NullString
		var rowid, fkid sql.NullInt64
		err := tx.QueryRow(`PRAGMA foreign_key_check`).Scan(&table, &rowid, &parent, &fkid)
		if err == nil {
			return fmt.Errorf("broken reference in %s row %d", table, rowid.Int64)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}

// Backup writes a consistent snapshot of the live database to dest using
// VACUUM INTO. Safe to run while the app is serving requests.
func (s *Store) Backup(ctx context.Context, dest string) error {
	_, err := s.DB.ExecContext(ctx, `VACUUM INTO ?`, dest)
	return err
}

// Settings ---------------------------------------------------------------

func (s *Store) Setting(key, fallback string) string {
	var v string
	if err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return fallback
	}
	return v
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SecretSetting is a sealed setting's value ("" when unset). It fails with
// secrets.ErrUnreadable when the value was sealed with another key (the
// database was restored onto a server without its secrets.key).
func (s *Store) SecretSetting(key string) (string, error) {
	return s.box.Open(s.Setting(key, ""))
}

// SetSecretSetting seals value and saves it; "" clears it.
func (s *Store) SetSecretSetting(key, value string) error {
	sealed, err := s.box.Seal(value)
	if err != nil {
		return err
	}
	return s.SetSetting(key, sealed)
}

// SealPlainSecrets seals any of keys still saved as plain text, e.g. from
// before secrets were sealed or a database edited by hand.
func (s *Store) SealPlainSecrets(keys ...string) error {
	for _, k := range keys {
		if v := s.Setting(k, ""); v != "" && !secrets.IsSealed(v) {
			if err := s.SetSecretSetting(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// helpers ----------------------------------------------------------------

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(i int64) any {
	if i == 0 {
		return nil
	}
	return i
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// DateLayout is how dates are stored: YYYY-MM-DD.
const DateLayout = "2006-01-02"
