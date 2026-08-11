package store

const schema = `
CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS api_keys (
 id TEXT PRIMARY KEY, digest BLOB NOT NULL, description TEXT NOT NULL DEFAULT '', scopes TEXT NOT NULL,
 created_at TEXT NOT NULL, last_used_at TEXT, revoked_at TEXT, revocation_reason TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS jobs (
 id TEXT PRIMARY KEY, principal_id TEXT NOT NULL, encrypted_url BLOB NOT NULL, url_scheme TEXT NOT NULL,
 host_fingerprint TEXT NOT NULL, status TEXT NOT NULL, verdict TEXT NOT NULL DEFAULT '', signature TEXT NOT NULL DEFAULT '',
 error_code TEXT NOT NULL DEFAULT '', attempts INTEGER NOT NULL DEFAULT 0, max_attempts INTEGER NOT NULL,
 bytes INTEGER NOT NULL DEFAULT 0, download_ms INTEGER NOT NULL DEFAULT 0, scan_ms INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL, started_at TEXT, completed_at TEXT, next_attempt_at TEXT NOT NULL,
 lease_owner TEXT NOT NULL DEFAULT '', lease_until TEXT
);
CREATE INDEX IF NOT EXISTS jobs_available ON jobs(status, next_attempt_at, created_at);
CREATE INDEX IF NOT EXISTS jobs_retention ON jobs(completed_at);
CREATE TABLE IF NOT EXISTS idempotency_keys (
 principal_id TEXT NOT NULL, idempotency_key TEXT NOT NULL, job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 expires_at TEXT NOT NULL, PRIMARY KEY(principal_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS hourly_stats (
 hour TEXT PRIMARY KEY, submitted INTEGER NOT NULL DEFAULT 0, completed INTEGER NOT NULL DEFAULT 0,
 clean INTEGER NOT NULL DEFAULT 0, infected INTEGER NOT NULL DEFAULT 0, failed INTEGER NOT NULL DEFAULT 0,
 retries INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0, download_ms INTEGER NOT NULL DEFAULT 0,
 scan_ms INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS outbox (
 event_id TEXT PRIMARY KEY, payload BLOB NOT NULL, created_at TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at TEXT NOT NULL, published_at TEXT, last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox(published_at, next_attempt_at);
CREATE TABLE IF NOT EXISTS workers (
 id TEXT PRIMARY KEY, heartbeat_at TEXT NOT NULL
);
INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, strftime('%Y-%m-%dT%H:%M:%fZ','now'));
`
