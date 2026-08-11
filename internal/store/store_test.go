package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/benachilds/avaas/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "avaas.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAPIKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	pepper := []byte("this is a sufficiently long test pepper value")
	key, token, err := GenerateKey(ctx, s, pepper, "build agent", []string{"scan", "admin"})
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || key.Description != "build agent" {
		t.Fatal("missing generated key data")
	}
	p, err := Authenticate(ctx, s, pepper, token)
	if err != nil || p.ID != key.ID || !p.Scopes["admin"] {
		t.Fatalf("authenticate: %#v %v", p, err)
	}
	if _, err = RevokeKey(ctx, s, key.ID, "retired", false); err == nil {
		t.Fatal("expected last-admin protection")
	}
	changed, err := RevokeKey(ctx, s, key.ID, "retired", true)
	if err != nil || !changed {
		t.Fatalf("revoke: %v", err)
	}
	if _, err = Authenticate(ctx, s, pepper, token); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("revoked key authenticated: %v", err)
	}
	keys, err := ListKeys(ctx, s, true)
	if err != nil || len(keys) != 1 || keys[0].RevokedAt == nil {
		t.Fatalf("list: %#v %v", keys, err)
	}
}

func TestJobLifecycleAndIdempotency(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UTC()
	j := model.Job{ID: "job-1", PrincipalID: "principal", EncryptedURL: []byte("secret"), URLScheme: "https", HostFingerprint: "host", Status: model.StatusQueued, MaxAttempts: 3, CreatedAt: now}
	e := model.Event{SchemaVersion: 1, EventID: "event-1", EventType: "scan.queued", JobID: j.ID, OccurredAt: now}
	created, existing, err := CreateJob(ctx, s, j, "same", time.Hour, e)
	if err != nil || existing || created.ID != j.ID {
		t.Fatalf("create: %#v %v", created, err)
	}
	duplicate, existing, err := CreateJob(ctx, s, model.Job{ID: "job-2", PrincipalID: "principal"}, "same", time.Hour, model.Event{})
	if err != nil || !existing || duplicate.ID != "job-1" {
		t.Fatalf("idempotency: %#v %v", duplicate, err)
	}
	claimed, err := ClaimJob(ctx, s, "worker", time.Minute)
	if err != nil || claimed.Attempts != 1 || claimed.Status != model.StatusDownloading {
		t.Fatalf("claim: %#v %v", claimed, err)
	}
	if err = SetScanning(ctx, s, j.ID, "worker", 1234, 25, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err = CompleteJob(ctx, s, j.ID, "worker", model.VerdictClean, "", "", 12); err != nil {
		t.Fatal(err)
	}
	finished, err := GetJob(ctx, s, j.ID)
	if err != nil || finished.Verdict != model.VerdictClean || finished.Bytes != 1234 {
		t.Fatalf("finish: %#v %v", finished, err)
	}
	encoded, err := json.Marshal(finished)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("next_attempt_at")) {
		t.Fatalf("terminal job exposed next attempt: %s", encoded)
	}
	stats, err := Stats(ctx, s, time.Time{})
	if err != nil || stats.Submitted != 1 || stats.Completed != 1 || stats.Clean != 1 {
		t.Fatalf("stats: %#v %v", stats, err)
	}
}

func TestOnlyOneWorkerCanClaim(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UTC()
	j := model.Job{ID: "job", PrincipalID: "p", EncryptedURL: []byte("x"), URLScheme: "https", HostFingerprint: "h", MaxAttempts: 1, CreatedAt: now}
	_, _, err := CreateJob(ctx, s, j, "", time.Hour, model.Event{SchemaVersion: 1, EventID: "evt", EventType: "scan.queued", JobID: "job", OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ClaimJob(ctx, s, "one", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err = ClaimJob(ctx, s, "two", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second claim = %v", err)
	}
}

func TestResetStatsPreservesJobsAndKeys(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	pepper := []byte("this is a sufficiently long test pepper value")
	key, _, err := GenerateKey(ctx, s, pepper, "admin", []string{"admin"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	j := model.Job{ID: "reset-job", PrincipalID: key.ID, EncryptedURL: []byte("secret"), URLScheme: "https", HostFingerprint: "host", MaxAttempts: 1, CreatedAt: now}
	_, _, err = CreateJob(ctx, s, j, "", time.Hour, model.Event{SchemaVersion: 1, EventID: "reset-event", EventType: "scan.queued", JobID: j.ID, OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	before, err := Stats(ctx, s, time.Time{})
	if err != nil || before.Submitted != 1 {
		t.Fatalf("before reset: %#v %v", before, err)
	}
	if _, err = ResetStats(ctx, s); err != nil {
		t.Fatal(err)
	}
	after, err := Stats(ctx, s, time.Time{})
	if err != nil || after.Submitted != 0 {
		t.Fatalf("after reset: %#v %v", after, err)
	}
	if _, err = GetJob(ctx, s, j.ID); err != nil {
		t.Fatalf("job removed: %v", err)
	}
	keys, err := ListKeys(ctx, s, false)
	if err != nil || len(keys) != 1 {
		t.Fatalf("key removed: %#v %v", keys, err)
	}
}

func TestDropPendingEventsWhenNotificationsDisabled(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now().UTC()
	j := model.Job{ID: "drop-job", PrincipalID: "principal", EncryptedURL: []byte("secret"), URLScheme: "https", HostFingerprint: "host", MaxAttempts: 1, CreatedAt: now}
	_, _, err := CreateJob(ctx, s, j, "", time.Hour, model.Event{SchemaVersion: 1, EventID: "drop-event", EventType: "scan.queued", JobID: j.ID, OccurredAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = PendingEvent(ctx, s); err != nil {
		t.Fatalf("pending event missing: %v", err)
	}
	if err = DropPendingEvents(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, err = PendingEvent(ctx, s); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled event retained: %v", err)
	}
}

func TestConcurrentMigrationsWaitForSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(path)
			if err == nil {
				err = s.Migrate(ctx)
				_ = s.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}
}
