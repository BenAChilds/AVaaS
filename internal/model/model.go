package model

import "time"

const (
	StatusQueued      = "queued"
	StatusDownloading = "downloading"
	StatusScanning    = "scanning"
	StatusRetrying    = "retrying"
	StatusCompleted   = "completed"

	VerdictClean    = "clean"
	VerdictInfected = "infected"
	VerdictError    = "error"
)

type Job struct {
	ID              string     `json:"id"`
	PrincipalID     string     `json:"-"`
	EncryptedURL    []byte     `json:"-"`
	URLScheme       string     `json:"-"`
	HostFingerprint string     `json:"-"`
	Status          string     `json:"status"`
	Verdict         string     `json:"verdict,omitempty"`
	Signature       string     `json:"signature,omitempty"`
	ErrorCode       string     `json:"error_code,omitempty"`
	Attempts        int        `json:"attempts"`
	MaxAttempts     int        `json:"max_attempts"`
	Bytes           int64      `json:"bytes,omitempty"`
	DownloadMS      int64      `json:"download_ms,omitempty"`
	ScanMS          int64      `json:"scan_ms,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	NextAttemptAt   time.Time  `json:"next_attempt_at,omitempty"`
	LeaseOwner      string     `json:"-"`
	LeaseUntil      *time.Time `json:"-"`
}

type APIKey struct {
	ID               string     `json:"id"`
	Description      string     `json:"description,omitempty"`
	Scopes           []string   `json:"scopes"`
	CreatedAt        time.Time  `json:"created_at"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevocationReason string     `json:"revocation_reason,omitempty"`
}

type QueueStats struct {
	Queued    int64 `json:"queued"`
	Active    int64 `json:"active"`
	Retrying  int64 `json:"retrying"`
	Scheduled int64 `json:"scheduled"`
	Total     int64 `json:"total"`
}

type AggregateStats struct {
	Since             time.Time `json:"since"`
	Submitted         int64     `json:"submitted"`
	Completed         int64     `json:"completed"`
	Clean             int64     `json:"clean"`
	Infected          int64     `json:"infected"`
	Failed            int64     `json:"failed"`
	Retries           int64     `json:"retries"`
	Bytes             int64     `json:"bytes_scanned"`
	AverageFileBytes  float64   `json:"average_file_bytes"`
	AverageDownloadMS float64   `json:"average_download_ms"`
	AverageScanMS     float64   `json:"average_scan_ms"`
	InfectionRate     float64   `json:"infection_rate"`
}

type Event struct {
	SchemaVersion int            `json:"schema_version"`
	EventID       string         `json:"event_id"`
	EventType     string         `json:"event_type"`
	JobID         string         `json:"job_id"`
	OccurredAt    time.Time      `json:"occurred_at"`
	Data          map[string]any `json:"data,omitempty"`
}
