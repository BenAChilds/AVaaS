package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/benachilds/avaas/internal/model"
)

func Stats(ctx context.Context, s *Store, since time.Time) (model.AggregateStats, error) {
	v := model.AggregateStats{Since: since.UTC()}
	var first sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT MIN(hour),COALESCE(SUM(submitted),0),COALESCE(SUM(completed),0),COALESCE(SUM(clean),0),COALESCE(SUM(infected),0),COALESCE(SUM(failed),0),COALESCE(SUM(retries),0),COALESCE(SUM(bytes),0),COALESCE(SUM(download_ms),0),COALESCE(SUM(scan_ms),0) FROM hourly_stats WHERE hour>=?`, hourText(since)).Scan(&first, &v.Submitted, &v.Completed, &v.Clean, &v.Infected, &v.Failed, &v.Retries, &v.Bytes, &v.AverageDownloadMS, &v.AverageScanMS)
	if err != nil {
		return v, err
	}
	if first.Valid && since.IsZero() {
		v.Since = parseTime(first.String)
	}
	if v.Completed > 0 {
		v.AverageFileBytes = float64(v.Bytes) / float64(v.Completed)
		v.AverageDownloadMS /= float64(v.Completed)
		v.AverageScanMS /= float64(v.Completed)
	}
	if scanned := v.Clean + v.Infected; scanned > 0 {
		v.InfectionRate = float64(v.Infected) / float64(scanned)
	}
	return v, nil
}

func Heartbeat(ctx context.Context, s *Store, id string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO workers(id,heartbeat_at) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET heartbeat_at=excluded.heartbeat_at`, id, nowText())
	return err
}
func WorkerHealthy(ctx context.Context, s *Store, maxAge time.Duration) bool {
	var t sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT MAX(heartbeat_at) FROM workers`).Scan(&t); err != nil || !t.Valid {
		return false
	}
	return parseTime(t.String).After(time.Now().Add(-maxAge))
}

func PendingEvent(ctx context.Context, s *Store) (model.Event, error) {
	var payload []byte
	err := s.DB.QueryRowContext(ctx, `SELECT payload FROM outbox WHERE published_at IS NULL AND next_attempt_at<=? ORDER BY created_at LIMIT 1`, nowText()).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Event{}, ErrNotFound
	}
	if err != nil {
		return model.Event{}, err
	}
	var e model.Event
	err = json.Unmarshal(payload, &e)
	return e, err
}
func EventPublished(ctx context.Context, s *Store, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE outbox SET published_at=?,last_error='' WHERE event_id=?`, nowText(), id)
	return err
}
func EventFailed(ctx context.Context, s *Store, id, msg string) error {
	if len(msg) > 200 {
		msg = msg[:200]
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE outbox SET attempts=attempts+1,last_error=?,next_attempt_at=? WHERE event_id=?`, msg, timeText(time.Now().Add(30*time.Second)), id)
	return err
}
func OutboxStats(ctx context.Context, s *Store) (pending, failures int64, last *time.Time, err error) {
	var lastText sql.NullString
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FILTER(WHERE published_at IS NULL),COALESCE(SUM(attempts),0),MAX(published_at) FROM outbox`).Scan(&pending, &failures, &lastText)
	if lastText.Valid {
		t := parseTime(lastText.String)
		last = &t
	}
	return
}
