package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddress       string
	PublicBaseURL       string
	DatabasePath        string
	PepperFile          string
	EncryptionKeyFile   string
	SpoolDir            string
	LogDir              string
	ClamdSocket         string
	MaxFileBytes        int64
	QueueLimit          int
	WorkerConcurrency   int
	MaxAttempts         int
	ResultRetention     time.Duration
	LeaseDuration       time.Duration
	DownloadTimeout     time.Duration
	DefinitionMaxAge    time.Duration
	LogMaxBytes         int64
	LogMaxFiles         int
	ValkeyEnabled       bool
	ValkeyHost          string
	ValkeyPort          int
	ValkeyUsername      string
	ValkeyPasswordFile  string
	ValkeyDatabase      int
	ValkeyTLS           bool
	ValkeyTLSServerName string
	ValkeyTopic         string
	OutboxLimit         int
}

func Default() Config {
	return Config{
		ListenAddress: ":8080", PublicBaseURL: "http://localhost:8080",
		DatabasePath: "/var/lib/avaas/avaas.db", PepperFile: "/etc/avaas/api-key.pepper",
		EncryptionKeyFile: "/etc/avaas/url-encryption.key", SpoolDir: "/var/spool/avaas",
		LogDir: "/var/log/avaas", ClamdSocket: "/run/avaas/clamd.sock",
		MaxFileBytes: 2 * 1024 * 1024 * 1024, QueueLimit: 1000, WorkerConcurrency: 1,
		MaxAttempts: 3, ResultRetention: 24 * time.Hour, LeaseDuration: 2 * time.Minute,
		DownloadTimeout: 2 * time.Hour, DefinitionMaxAge: 24 * time.Hour,
		LogMaxBytes: 20 * 1024 * 1024, LogMaxFiles: 5,
		ValkeyPort: 6379, ValkeyTopic: "avaas.events", OutboxLimit: 10000,
	}
}

func Load(path string) (Config, error) {
	if path != "" {
		if err := loadEnvFile(path); err != nil {
			return Config{}, err
		}
	}
	c := Default()
	c.ListenAddress = env("AVAAS_LISTEN_ADDRESS", c.ListenAddress)
	c.PublicBaseURL = strings.TrimRight(env("AVAAS_PUBLIC_BASE_URL", c.PublicBaseURL), "/")
	c.DatabasePath = env("AVAAS_DATABASE_PATH", c.DatabasePath)
	c.PepperFile = env("AVAAS_PEPPER_FILE", c.PepperFile)
	c.EncryptionKeyFile = env("AVAAS_ENCRYPTION_KEY_FILE", c.EncryptionKeyFile)
	c.SpoolDir = env("AVAAS_SPOOL_DIR", c.SpoolDir)
	c.LogDir = env("AVAAS_LOG_DIR", c.LogDir)
	c.ClamdSocket = env("AVAAS_CLAMD_SOCKET", c.ClamdSocket)
	var err error
	if c.MaxFileBytes, err = envInt64("AVAAS_MAX_FILE_BYTES", c.MaxFileBytes); err != nil {
		return c, err
	}
	if c.QueueLimit, err = envInt("AVAAS_QUEUE_LIMIT", c.QueueLimit); err != nil {
		return c, err
	}
	if c.WorkerConcurrency, err = envInt("AVAAS_WORKER_CONCURRENCY", c.WorkerConcurrency); err != nil {
		return c, err
	}
	if c.MaxAttempts, err = envInt("AVAAS_MAX_ATTEMPTS", c.MaxAttempts); err != nil {
		return c, err
	}
	if c.ResultRetention, err = envDuration("AVAAS_RESULT_RETENTION", c.ResultRetention); err != nil {
		return c, err
	}
	if c.LeaseDuration, err = envDuration("AVAAS_LEASE_DURATION", c.LeaseDuration); err != nil {
		return c, err
	}
	if c.DownloadTimeout, err = envDuration("AVAAS_DOWNLOAD_TIMEOUT", c.DownloadTimeout); err != nil {
		return c, err
	}
	if c.DefinitionMaxAge, err = envDuration("AVAAS_DEFINITION_MAX_AGE", c.DefinitionMaxAge); err != nil {
		return c, err
	}
	if c.LogMaxBytes, err = envInt64("AVAAS_LOG_MAX_BYTES", c.LogMaxBytes); err != nil {
		return c, err
	}
	if c.LogMaxFiles, err = envInt("AVAAS_LOG_MAX_FILES", c.LogMaxFiles); err != nil {
		return c, err
	}
	if c.ValkeyEnabled, err = envBool("AVAAS_VALKEY_ENABLED", false); err != nil {
		return c, err
	}
	c.ValkeyHost = env("AVAAS_VALKEY_HOST", "")
	if c.ValkeyPort, err = envInt("AVAAS_VALKEY_PORT", c.ValkeyPort); err != nil {
		return c, err
	}
	c.ValkeyUsername = env("AVAAS_VALKEY_USERNAME", "")
	c.ValkeyPasswordFile = env("AVAAS_VALKEY_PASSWORD_FILE", "")
	if c.ValkeyDatabase, err = envInt("AVAAS_VALKEY_DATABASE", 0); err != nil {
		return c, err
	}
	if c.ValkeyTLS, err = envBool("AVAAS_VALKEY_TLS", false); err != nil {
		return c, err
	}
	c.ValkeyTLSServerName = env("AVAAS_VALKEY_TLS_SERVER_NAME", "")
	c.ValkeyTopic = env("AVAAS_VALKEY_TOPIC", c.ValkeyTopic)
	if c.OutboxLimit, err = envInt("AVAAS_OUTBOX_LIMIT", c.OutboxLimit); err != nil {
		return c, err
	}
	if c.QueueLimit < 1 || c.WorkerConcurrency < 1 || c.MaxAttempts < 1 || c.MaxFileBytes < 1 {
		return c, errors.New("queue limit, concurrency, attempts, and max file bytes must be positive")
	}
	if c.ValkeyEnabled && (c.ValkeyHost == "" || c.ValkeyTopic == "") {
		return c, errors.New("Valkey host and topic are required when Valkey is enabled")
	}
	return c, nil
}

func loadEnvFile(path string) error {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid config line %q", line)
		}
		k, v = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(v), "\"'")
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
	return s.Err()
}

func env(k, d string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return d
}
func envInt(k string, d int) (int, error) {
	v := env(k, "")
	if v == "" {
		return d, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil {
		return 0, fmt.Errorf("%s: %w", k, e)
	}
	return n, nil
}
func envInt64(k string, d int64) (int64, error) {
	v := env(k, "")
	if v == "" {
		return d, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil {
		return 0, fmt.Errorf("%s: %w", k, e)
	}
	return n, nil
}
func envBool(k string, d bool) (bool, error) {
	v := env(k, "")
	if v == "" {
		return d, nil
	}
	n, e := strconv.ParseBool(v)
	if e != nil {
		return false, fmt.Errorf("%s: %w", k, e)
	}
	return n, nil
}
func envDuration(k string, d time.Duration) (time.Duration, error) {
	v := env(k, "")
	if v == "" {
		return d, nil
	}
	n, e := time.ParseDuration(v)
	if e != nil {
		return 0, fmt.Errorf("%s: %w", k, e)
	}
	return n, nil
}

func ReadSecret(path string, want int) ([]byte, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	b = []byte(strings.TrimSpace(string(b)))
	if want > 0 && len(b) < want {
		return nil, fmt.Errorf("secret %s must contain at least %d bytes", path, want)
	}
	return b, nil
}
