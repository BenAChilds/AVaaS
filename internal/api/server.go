package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/benachilds/avaas/internal/auth"
	"github.com/benachilds/avaas/internal/clamav"
	"github.com/benachilds/avaas/internal/config"
	"github.com/benachilds/avaas/internal/download"
	logstore "github.com/benachilds/avaas/internal/logging"
	"github.com/benachilds/avaas/internal/model"
	"github.com/benachilds/avaas/internal/store"
	"github.com/benachilds/avaas/internal/updatecheck"
)

type principalKey struct{}
type Server struct {
	cfg          config.Config
	store        *store.Store
	pepper       []byte
	cipher       *auth.URLCipher
	clam         clamav.Client
	log          *slog.Logger
	logLimits    *limiter
	submitLimits *limiter
	updates      updatecheck.Provider
}

type Option func(*Server)

func WithUpdateProvider(provider updatecheck.Provider) Option {
	return func(s *Server) { s.updates = provider }
}

func New(cfg config.Config, s *store.Store, pepper []byte, cipher *auth.URLCipher, log *slog.Logger, options ...Option) *Server {
	server := &Server{cfg: cfg, store: s, pepper: pepper, cipher: cipher, clam: clamav.Client{Socket: cfg.ClamdSocket, Timeout: 10 * time.Second}, log: log, logLimits: newLimiter(10, time.Minute), submitLimits: newLimiter(60, time.Minute), updates: updatecheck.New(false, cfg.UpdateCheckInterval, "dev", nil, log)}
	for _, option := range options {
		option(server)
	}
	return server
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.Handle("POST /v1/scans", s.authn(http.HandlerFunc(s.createScan)))
	mux.Handle("GET /v1/scans/{id}", s.authn(http.HandlerFunc(s.getScan)))
	mux.Handle("GET /v1/queue", s.authn(http.HandlerFunc(s.queue)))
	mux.Handle("GET /v1/system/clamav", s.authn(http.HandlerFunc(s.clamVersion)))
	mux.Handle("GET /v1/stats", s.authn(http.HandlerFunc(s.stats)))
	mux.Handle("GET /v1/logs", s.authn(http.HandlerFunc(s.logs)))
	return s.security(mux)
}
func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) authn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer API key is required")
			return
		}
		p, err := store.Authenticate(r.Context(), s.store, s.pepper, strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")))
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer API key is required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
func principal(r *http.Request) store.Principal {
	return r.Context().Value(principalKey{}).(store.Principal)
}
func require(w http.ResponseWriter, p store.Principal, scope string) bool {
	if store.RequireScope(p, scope) != nil {
		writeError(w, http.StatusForbidden, "forbidden", "insufficient scope")
		return false
	}
	return true
}

func (s *Server) createScan(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if !require(w, p, "scan") {
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if host == "" {
		host = r.RemoteAddr
	}
	if !s.submitLimits.allow("key:"+p.ID) || !s.submitLimits.allow("ip:"+host) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many scan requests")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	defer func() { _ = r.Body.Close() }()
	var in struct {
		URL string `json:"url"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, 400, "invalid_request", "request body must contain one URL")
		return
	}
	u, err := url.Parse(in.URL)
	if err != nil || download.ValidateURL(u) != nil {
		writeError(w, 400, "invalid_url", "only HTTP and HTTPS URLs are accepted")
		return
	}
	q, err := store.QueueStats(r.Context(), s.store)
	if err != nil {
		writeError(w, 503, "database_unavailable", "service temporarily unavailable")
		return
	}
	if q.Total >= int64(s.cfg.QueueLimit) {
		writeError(w, 429, "queue_full", "the scan queue is full")
		return
	}
	enc, err := s.cipher.Encrypt(in.URL)
	if err != nil {
		writeError(w, 500, "internal_error", "unable to create scan")
		return
	}
	now := time.Now().UTC()
	id := randomID()
	job := model.Job{ID: id, PrincipalID: p.ID, EncryptedURL: enc, URLScheme: u.Scheme, HostFingerprint: auth.FingerprintHost(in.URL), Status: model.StatusQueued, MaxAttempts: s.cfg.MaxAttempts, CreatedAt: now}
	event := model.Event{SchemaVersion: 1, EventID: randomID(), EventType: "scan.queued", JobID: id, OccurredAt: now, Data: map[string]any{"status": model.StatusQueued}}
	idem := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(idem) > 200 {
		writeError(w, 400, "invalid_idempotency_key", "idempotency key is too long")
		return
	}
	job, existing, err := store.CreateJob(r.Context(), s.store, job, idem, s.cfg.ResultRetention, event)
	if err != nil {
		writeError(w, 500, "database_error", "unable to create scan")
		return
	}
	location := s.cfg.PublicBaseURL + "/v1/scans/" + job.ID
	if existing {
		s.log.Info("scan submission deduplicated", "job_id", job.ID)
	} else {
		s.log.Info("scan queued", "job_id", job.ID)
	}
	w.Header().Set("Location", location)
	writeJSON(w, 202, map[string]any{"id": job.ID, "status": job.Status, "status_url": location})
}
func (s *Server) getScan(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	j, err := store.GetJob(r.Context(), s.store, r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "scan was not found")
		return
	}
	if err != nil {
		writeError(w, 500, "database_error", "unable to retrieve scan")
		return
	}
	if j.PrincipalID != p.ID && !p.Scopes["admin"] {
		writeError(w, 404, "not_found", "scan was not found")
		return
	}
	writeJSON(w, 200, j)
}
func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	if !require(w, principal(r), "admin") {
		return
	}
	v, err := store.QueueStats(r.Context(), s.store)
	if err != nil {
		writeError(w, 500, "database_error", "unable to retrieve queue")
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) clamVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.clam.Version(r.Context())
	if err != nil {
		writeError(w, 503, "clamav_unavailable", "ClamAV is unavailable")
		return
	}
	info := clamav.ParseVersion(v)
	out := map[string]any{"raw": logstore.Sanitize(info.Raw), "ready": true, "engine_version": info.EngineVersion, "definition_version": info.DefinitionVersion, "definition_date": info.DefinitionDate}
	writeJSON(w, 200, out)
}
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	if !require(w, principal(r), "admin") {
		return
	}
	lifetime, err := store.Stats(r.Context(), s.store, time.Time{})
	if err != nil {
		writeError(w, 500, "database_error", "unable to retrieve statistics")
		return
	}
	daily, _ := store.Stats(r.Context(), s.store, time.Now().Add(-24*time.Hour))
	notifications := map[string]any{"enabled": false, "pending": int64(0), "publish_failures": int64(0), "last_published_at": nil}
	if s.cfg.ValkeyEnabled {
		pending, failures, last, _ := store.OutboxStats(r.Context(), s.store)
		notifications = map[string]any{"enabled": true, "pending": pending, "publish_failures": failures, "last_published_at": last}
	}
	writeJSON(w, 200, map[string]any{"lifetime": lifetime, "last_24_hours": daily, "notifications": notifications})
}
func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if !s.logLimits.allow(p.ID) {
		writeError(w, 429, "rate_limited", "too many log requests")
		return
	}
	q := logstore.Query{Source: r.URL.Query().Get("source"), Level: r.URL.Query().Get("level"), Cursor: r.URL.Query().Get("cursor"), Limit: 100}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeError(w, 400, "invalid_limit", "invalid limit")
			return
		}
		q.Limit = n
	}
	var err error
	if v := r.URL.Query().Get("since"); v != "" {
		q.Since, err = time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, 400, "invalid_since", "since must be RFC3339")
			return
		}
	} else {
		q.Since = time.Now().Add(-time.Hour)
	}
	if q.Since.Before(time.Now().Add(-7 * 24 * time.Hour)) {
		writeError(w, 400, "time_range_too_large", "logs are limited to the last seven days")
		return
	}
	if v := r.URL.Query().Get("until"); v != "" {
		q.Until, err = time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, 400, "invalid_until", "until must be RFC3339")
			return
		}
	}
	result, err := logstore.Read(r.Context(), s.cfg.LogDir, q)
	if errors.Is(err, logstore.ErrCursorExpired) {
		writeError(w, 410, "cursor_expired", "the log cursor is no longer valid")
		return
	}
	if errors.Is(err, logstore.ErrLogUnreadable) {
		writeError(w, 500, "logs_unavailable", "one or more requested log sources cannot be read")
		return
	}
	if err != nil {
		writeError(w, 400, "invalid_log_query", err.Error())
		return
	}
	s.log.Info("log retrieval audit", "source", q.Source, "count", len(result.Records))
	writeJSON(w, 200, result)
}
func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "alive"})
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	version, versionErr := s.clam.Version(ctx)
	definitionsFresh := false
	if versionErr == nil {
		info := clamav.ParseVersion(version)
		definitionsFresh = info.DefinitionDate == nil || info.DefinitionDate.After(time.Now().Add(-s.cfg.DefinitionMaxAge))
	}
	checks := map[string]bool{"database": s.store.Ping(ctx) == nil, "worker": store.WorkerHealthy(ctx, s.store, 30*time.Second), "clamav": versionErr == nil, "definitions_fresh": definitionsFresh, "spool_capacity": spoolCapacity(s.cfg.SpoolDir, s.cfg.MaxFileBytes)}
	ok := true
	for _, v := range checks {
		ok = ok && v
	}
	status := 200
	if !ok {
		status = 503
	}
	writeJSON(w, status, map[string]any{"ready": ok, "checks": checks, "update": s.updates.Snapshot()})
}

func spoolCapacity(path string, minimum int64) bool {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return false
	}
	return int64(stat.Bavail)*int64(stat.Bsize) >= minimum
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

type limiter struct {
	mu      sync.Mutex
	n       int
	window  time.Duration
	entries map[string]*limitEntry
}
type limitEntry struct {
	start time.Time
	count int
}

func newLimiter(n int, w time.Duration) *limiter {
	return &limiter{n: n, window: w, entries: map[string]*limitEntry{}}
}
func (l *limiter) allow(k string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	e := l.entries[k]
	if e == nil || now.Sub(e.start) >= l.window {
		l.entries[k] = &limitEntry{start: now, count: 1}
		return true
	}
	if e.count >= l.n {
		return false
	}
	e.count++
	return true
}
