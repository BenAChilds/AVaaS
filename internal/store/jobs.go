package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/benachilds/avaas/internal/model"
)

func CreateJob(ctx context.Context, s *Store, j model.Job, idem string, idemTTL time.Duration, event model.Event) (model.Job, bool, error) {
	var existing bool
	err := withTx(ctx, s.DB, func(tx *sql.Tx) error {
		if idem != "" {
			var id string
			var expires string
			err := tx.QueryRowContext(ctx, `SELECT job_id,expires_at FROM idempotency_keys WHERE principal_id=? AND idempotency_key=?`, j.PrincipalID, idem).Scan(&id, &expires)
			if err == nil && parseTime(expires).After(time.Now()) {
				found, e := getJob(ctx, tx, id)
				if e != nil {
					return e
				}
				j = found
				existing = true
				return nil
			}
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,principal_id,encrypted_url,url_scheme,host_fingerprint,status,max_attempts,created_at,next_attempt_at) VALUES(?,?,?,?,?,?,?,?,?)`, j.ID, j.PrincipalID, j.EncryptedURL, j.URLScheme, j.HostFingerprint, model.StatusQueued, j.MaxAttempts, timeText(j.CreatedAt), timeText(j.CreatedAt))
		if err != nil {
			return err
		}
		if idem != "" {
			_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO idempotency_keys(principal_id,idempotency_key,job_id,expires_at) VALUES(?,?,?,?)`, j.PrincipalID, idem, j.ID, timeText(time.Now().Add(idemTTL)))
			if err != nil {
				return err
			}
		}
		if err = incrementStats(ctx, tx, "submitted", 1, j.CreatedAt); err != nil {
			return err
		}
		return insertEvent(ctx, tx, event)
	})
	return j, existing, err
}

func getJob(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (model.Job, error) {
	var j model.Job
	var created, next string
	var started, completed, lease sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id,principal_id,encrypted_url,url_scheme,host_fingerprint,status,verdict,signature,error_code,attempts,max_attempts,bytes,download_ms,scan_ms,created_at,started_at,completed_at,next_attempt_at,lease_owner,lease_until FROM jobs WHERE id=?`, id).Scan(&j.ID, &j.PrincipalID, &j.EncryptedURL, &j.URLScheme, &j.HostFingerprint, &j.Status, &j.Verdict, &j.Signature, &j.ErrorCode, &j.Attempts, &j.MaxAttempts, &j.Bytes, &j.DownloadMS, &j.ScanMS, &created, &started, &completed, &next, &j.LeaseOwner, &lease)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	j.CreatedAt = parseTime(created)
	j.StartedAt = nullableTime(started)
	j.CompletedAt = nullableTime(completed)
	if j.Status != model.StatusCompleted {
		nextAt := parseTime(next)
		j.NextAttemptAt = &nextAt
	}
	j.LeaseUntil = nullableTime(lease)
	return j, nil
}
func GetJob(ctx context.Context, s *Store, id string) (model.Job, error) {
	return getJob(ctx, s.DB, id)
}

func ClaimJob(ctx context.Context, s *Store, worker string, lease time.Duration) (model.Job, error) {
	var out model.Job
	err := withTx(ctx, s.DB, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		row := tx.QueryRowContext(ctx, `UPDATE jobs SET status=?,lease_owner=?,lease_until=?,attempts=attempts+1,started_at=COALESCE(started_at,?)
WHERE id=(SELECT id FROM jobs WHERE ((status IN ('queued','retrying') AND next_attempt_at<=?) OR (status IN ('downloading','scanning') AND lease_until<?)) ORDER BY next_attempt_at,created_at LIMIT 1)
RETURNING id`, model.StatusDownloading, worker, timeText(now.Add(lease)), timeText(now), timeText(now), timeText(now))
		var id string
		if err := row.Scan(&id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		claimed, getErr := getJob(ctx, tx, id)
		if getErr != nil {
			return getErr
		}
		out = claimed
		e := model.Event{SchemaVersion: 1, EventID: newID(), EventType: "scan.started", JobID: id, OccurredAt: now, Data: map[string]any{"attempt": out.Attempts}}
		return insertEvent(ctx, tx, e)
	})
	return out, err
}

func SetScanning(ctx context.Context, s *Store, id, worker string, bytes, downloadMS int64, lease time.Duration) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE jobs SET status=?,bytes=?,download_ms=?,lease_until=? WHERE id=? AND lease_owner=?`, model.StatusScanning, bytes, downloadMS, timeText(time.Now().Add(lease)), id, worker)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
func RenewLease(ctx context.Context, s *Store, id, worker string, lease time.Duration) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE jobs SET lease_until=? WHERE id=? AND lease_owner=? AND status IN ('downloading','scanning')`, timeText(time.Now().Add(lease)), id, worker)
	return err
}

func CompleteJob(ctx context.Context, s *Store, id, worker, verdict, signature, errorCode string, scanMS int64) error {
	return withTx(ctx, s.DB, func(tx *sql.Tx) error {
		now := time.Now().UTC()
		res, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?,verdict=?,signature=?,error_code=?,scan_ms=?,completed_at=?,lease_owner='',lease_until=NULL WHERE id=? AND lease_owner=? AND status!='completed'`, model.StatusCompleted, verdict, signature, errorCode, scanMS, timeText(now), id, worker)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
		j, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if err = incrementStats(ctx, tx, "completed", 1, now); err != nil {
			return err
		}
		field := verdict
		if verdict == model.VerdictError {
			field = "failed"
		}
		if err = incrementStats(ctx, tx, field, 1, now); err != nil {
			return err
		}
		for field, val := range map[string]int64{"bytes": j.Bytes, "download_ms": j.DownloadMS, "scan_ms": j.ScanMS} {
			if err = incrementStats(ctx, tx, field, val, now); err != nil {
				return err
			}
		}
		e := model.Event{SchemaVersion: 1, EventID: newID(), EventType: "scan.completed", JobID: id, OccurredAt: now, Data: map[string]any{"verdict": verdict, "bytes": j.Bytes, "scan_ms": scanMS}}
		if signature != "" {
			e.Data["signature"] = signature
		}
		return insertEvent(ctx, tx, e)
	})
}

func RetryJob(ctx context.Context, s *Store, id, worker, errorCode string, backoff time.Duration) error {
	return withTx(ctx, s.DB, func(tx *sql.Tx) error {
		j, err := getJob(ctx, tx, id)
		if err != nil {
			return err
		}
		if j.Attempts >= j.MaxAttempts {
			return CompleteJobTx(ctx, tx, j, worker, errorCode)
		}
		now := time.Now().UTC()
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET status=?,error_code=?,next_attempt_at=?,lease_owner='',lease_until=NULL WHERE id=? AND lease_owner=?`, model.StatusRetrying, errorCode, timeText(now.Add(backoff)), id, worker)
		if err != nil {
			return err
		}
		if err = incrementStats(ctx, tx, "retries", 1, now); err != nil {
			return err
		}
		return insertEvent(ctx, tx, model.Event{SchemaVersion: 1, EventID: newID(), EventType: "scan.retrying", JobID: id, OccurredAt: now, Data: map[string]any{"attempt": j.Attempts, "error_code": errorCode}})
	})
}
func CompleteJobTx(ctx context.Context, tx *sql.Tx, j model.Job, worker, errorCode string) error {
	now := time.Now().UTC()
	_, err := tx.ExecContext(ctx, `UPDATE jobs SET status=?,verdict=?,error_code=?,completed_at=?,lease_owner='',lease_until=NULL WHERE id=? AND lease_owner=?`, model.StatusCompleted, model.VerdictError, errorCode, timeText(now), j.ID, worker)
	if err != nil {
		return err
	}
	if err = incrementStats(ctx, tx, "completed", 1, now); err != nil {
		return err
	}
	if err = incrementStats(ctx, tx, "failed", 1, now); err != nil {
		return err
	}
	return insertEvent(ctx, tx, model.Event{SchemaVersion: 1, EventID: newID(), EventType: "scan.completed", JobID: j.ID, OccurredAt: now, Data: map[string]any{"verdict": model.VerdictError, "error_code": errorCode}})
}

func QueueStats(ctx context.Context, s *Store) (model.QueueStats, error) {
	var v model.QueueStats
	rows, err := s.DB.QueryContext(ctx, `SELECT status,COUNT(*) FROM jobs WHERE status!='completed' GROUP BY status`)
	if err != nil {
		return v, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return v, err
		}
		switch st {
		case model.StatusQueued:
			v.Queued = n
		case model.StatusDownloading, model.StatusScanning:
			v.Active += n
		case model.StatusRetrying:
			v.Retrying = n
		}
	}
	v.Total = v.Queued + v.Active + v.Retrying + v.Scheduled
	return v, rows.Err()
}

func incrementStats(ctx context.Context, tx *sql.Tx, field string, n int64, t time.Time) error {
	allowed := map[string]bool{"submitted": true, "completed": true, "clean": true, "infected": true, "failed": true, "retries": true, "bytes": true, "download_ms": true, "scan_ms": true}
	if !allowed[field] {
		return fmt.Errorf("invalid stats field")
	}
	q := fmt.Sprintf(`INSERT INTO hourly_stats(hour,%s) VALUES(?,?) ON CONFLICT(hour) DO UPDATE SET %s=%s+excluded.%s`, field, field, field, field)
	_, err := tx.ExecContext(ctx, q, hourText(t), n)
	return err
}

func insertEvent(ctx context.Context, tx *sql.Tx, e model.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO outbox(event_id,payload,created_at,next_attempt_at) VALUES(?,?,?,?)`, e.EventID, b, timeText(e.OccurredAt), timeText(e.OccurredAt)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM outbox WHERE event_id IN (SELECT event_id FROM outbox ORDER BY created_at LIMIT MAX(0,(SELECT COUNT(*) FROM outbox)-10000))`)
	return err
}
func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

func Cleanup(ctx context.Context, s *Store, retention time.Duration) error {
	cut := timeText(time.Now().Add(-retention))
	_, err := s.DB.ExecContext(ctx, `DELETE FROM jobs WHERE completed_at IS NOT NULL AND completed_at<?; DELETE FROM idempotency_keys WHERE expires_at<?; DELETE FROM outbox WHERE published_at IS NOT NULL AND published_at<?`, cut, nowText(), cut)
	return err
}
