package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct{ DB *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.Clean(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxLifetime(0)
	if err := retryBusy(context.Background(), func() error { return db.Ping() }); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Migrate(ctx context.Context) error {
	return retryBusy(ctx, func() error { _, err := s.DB.ExecContext(ctx, schema); return err })
}
func (s *Store) Ping(ctx context.Context) error { return s.DB.PingContext(ctx) }

func nowText() string              { return time.Now().UTC().Format(time.RFC3339Nano) }
func timeText(t time.Time) string  { return t.UTC().Format(time.RFC3339Nano) }
func parseTime(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }
func nullableTime(v sql.NullString) *time.Time {
	if !v.Valid {
		return nil
	}
	t := parseTime(v.String)
	return &t
}
func scopesText(v []string) string { return strings.Join(v, ",") }
func parseScopes(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}
func hourText(t time.Time) string { return t.UTC().Truncate(time.Hour).Format(time.RFC3339) }

func retryBusy(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		lower := strings.ToLower(err.Error())
		if !strings.Contains(lower, "locked") && !strings.Contains(lower, "busy") {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return fmt.Errorf("database remained busy: %w", err)
}

func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
