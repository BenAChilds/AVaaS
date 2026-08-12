package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/benachilds/avaas/internal/auth"
	"github.com/benachilds/avaas/internal/config"
	"github.com/benachilds/avaas/internal/store"
	"github.com/benachilds/avaas/internal/updatecheck"
)

func testServer(t *testing.T, options ...Option) (http.Handler, *store.Store, []byte, string, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DatabasePath = filepath.Join(dir, "avaas.db")
	cfg.LogDir = filepath.Join(dir, "logs")
	cfg.SpoolDir = filepath.Join(dir, "spool")
	cfg.DefinitionUpdateTrigger = filepath.Join(dir, "run", "definition-update.request")
	if err := os.MkdirAll(filepath.Dir(cfg.DefinitionUpdateTrigger), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg.PublicBaseURL = "https://scan.example"
	s, err := store.Open(cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	pepper := []byte("a sufficiently long API pepper for tests")
	cipher, err := auth.NewURLCipher([]byte("a sufficiently long encryption key for tests"))
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.GenerateKey(context.Background(), s, pepper, "test", []string{"scan", "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, s, pepper, cipher, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), options...).Handler(), s, pepper, token, cfg.LogDir
}

func TestDefinitionUpdateTrigger(t *testing.T) {
	h, s, pepper, token, _ := testServer(t)
	unauthorized := request(t, h, http.MethodPost, "/v1/system/clamav/definitions/update", "", nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status %d", unauthorized.Code)
	}
	_, scanToken, err := store.GenerateKey(context.Background(), s, pepper, "scan only", []string{"scan"})
	if err != nil {
		t.Fatal(err)
	}
	forbidden := request(t, h, http.MethodPost, "/v1/system/clamav/definitions/update", scanToken, nil)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("forbidden status %d", forbidden.Code)
	}
	invalid := request(t, h, http.MethodPost, "/v1/system/clamav/definitions/update", token, []byte(`{}`))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("body status %d", invalid.Code)
	}
	accepted := request(t, h, http.MethodPost, "/v1/system/clamav/definitions/update", token, nil)
	if accepted.Code != http.StatusAccepted || !bytes.Contains(accepted.Body.Bytes(), []byte(`"status":"accepted"`)) {
		t.Fatalf("accepted: %d %s", accepted.Code, accepted.Body.String())
	}
	pending := request(t, h, http.MethodPost, "/v1/system/clamav/definitions/update", token, nil)
	if pending.Code != http.StatusAccepted || !bytes.Contains(pending.Body.Bytes(), []byte(`"status":"pending"`)) {
		t.Fatalf("pending: %d %s", pending.Code, pending.Body.String())
	}
}
func request(t *testing.T, h http.Handler, method, path, token string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

type staticUpdates struct{ status updatecheck.Status }

func (s staticUpdates) Snapshot() updatecheck.Status { return s.status }

func TestHealthIncludesInformationalUpdateStatus(t *testing.T) {
	checked := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
	updates := staticUpdates{status: updatecheck.Status{CurrentVersion: "v0.5.2b", Channel: "beta", Status: "available", Available: true, LatestVersion: "v0.5.3b", ReleaseURL: "https://github.com/BenAChilds/AVaaS/releases/tag/v0.5.3b", CheckedAt: &checked}}
	h, _, _, _, _ := testServer(t, WithUpdateProvider(updates))
	w := request(t, h, http.MethodGet, "/health/ready", "", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("dependency readiness unexpectedly changed: %d", w.Code)
	}
	for _, expected := range []string{`"status":"available"`, `"available":true`, `"latest_version":"v0.5.3b"`} {
		if !bytes.Contains(w.Body.Bytes(), []byte(expected)) {
			t.Fatalf("missing %s in %s", expected, w.Body.String())
		}
	}
}

func TestSubmitPollQueueAndRevoke(t *testing.T) {
	h, s, _, token, _ := testServer(t)
	unauthorized := request(t, h, http.MethodPost, "/v1/scans", "", []byte(`{"url":"https://example.org/file"}`))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status %d", unauthorized.Code)
	}
	created := request(t, h, http.MethodPost, "/v1/scans", token, []byte(`{"url":"https://example.org/file"}`))
	if created.Code != http.StatusAccepted {
		t.Fatalf("create status %d: %s", created.Code, created.Body.String())
	}
	var submission map[string]string
	if err := json.Unmarshal(created.Body.Bytes(), &submission); err != nil {
		t.Fatal(err)
	}
	if submission["status_url"] != "https://scan.example/v1/scans/"+submission["id"] {
		t.Fatalf("bad status URL: %#v", submission)
	}
	polled := request(t, h, http.MethodGet, "/v1/scans/"+submission["id"], token, nil)
	if polled.Code != http.StatusOK || bytes.Contains(polled.Body.Bytes(), []byte("example.org")) {
		t.Fatalf("poll leaked URL or failed: %d %s", polled.Code, polled.Body.String())
	}
	queue := request(t, h, http.MethodGet, "/v1/queue", token, nil)
	if queue.Code != http.StatusOK || !bytes.Contains(queue.Body.Bytes(), []byte(`"queued":1`)) {
		t.Fatalf("queue: %d %s", queue.Code, queue.Body.String())
	}
	stats := request(t, h, http.MethodGet, "/v1/stats", token, nil)
	if stats.Code != http.StatusOK || !bytes.Contains(stats.Body.Bytes(), []byte(`"enabled":false`)) || !bytes.Contains(stats.Body.Bytes(), []byte(`"last_published_at":null`)) {
		t.Fatalf("disabled notification stats: %d %s", stats.Code, stats.Body.String())
	}
	keys, err := store.ListKeys(context.Background(), s, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.RevokeKey(context.Background(), s, keys[0].ID, "test", true); err != nil {
		t.Fatal(err)
	}
	revoked := request(t, h, http.MethodGet, "/v1/queue", token, nil)
	if revoked.Code != http.StatusUnauthorized {
		t.Fatalf("revoked status %d", revoked.Code)
	}
}

func TestRejectsUnsafeInputBeforeQueue(t *testing.T) {
	h, _, _, token, _ := testServer(t)
	for _, body := range []string{`{"url":"file:///etc/passwd"}`, `{"url":"https://user:pass@example.org/file"}`, `{"url":"https://example.org","extra":true}`} {
		w := request(t, h, http.MethodPost, "/v1/scans", token, []byte(body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s returned %d", body, w.Code)
		}
	}
}

func TestLogsReportUnreadableSource(t *testing.T) {
	h, _, _, token, logDir := testServer(t)
	if err := os.MkdirAll(filepath.Join(logDir, "worker.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := request(t, h, http.MethodGet, "/v1/logs?source=worker", token, nil)
	if w.Code != http.StatusInternalServerError || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"logs_unavailable"`)) {
		t.Fatalf("unreadable logs: %d %s", w.Code, w.Body.String())
	}
}
