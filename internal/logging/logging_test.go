package logging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSanitize(t *testing.T) {
	in := "download https://bucket.example/file?X-Amz-Signature=secret Authorization: Bearer avaas_identifier.secret /var/spool/avaas/file.scan"
	got := Sanitize(in)
	for _, secret := range []string{"bucket.example", "X-Amz", "avaas_identifier", "file.scan"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sensitive value %q survived: %s", secret, got)
		}
	}
}

func TestReadAPIAndWorkerLogsIncludingRotation(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "api.jsonl.1"), "2026-08-12T00:00:00Z", "INFO", "api rotated")
	writeLog(t, filepath.Join(dir, "worker.jsonl"), "2026-08-12T00:02:00Z", "ERROR", "worker current")
	writeLog(t, filepath.Join(dir, "api.jsonl"), "2026-08-12T00:01:00Z", "WARN", "api current")

	api, err := Read(context.Background(), dir, Query{Source: "api", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(api.Records) != 2 || api.Records[0].Message != "api rotated" || api.Records[1].Message != "api current" {
		t.Fatalf("unexpected API records: %#v", api.Records)
	}
	worker, err := Read(context.Background(), dir, Query{Source: "worker", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(worker.Records) != 1 || worker.Records[0].Source != "worker" {
		t.Fatalf("unexpected worker records: %#v", worker.Records)
	}
	all, err := Read(context.Background(), dir, Query{Source: "all", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Records) != 3 || all.Records[0].Message != "api rotated" || all.Records[2].Message != "worker current" {
		t.Fatalf("logs were not chronologically merged: %#v", all.Records)
	}
}

func TestReadFiltersSanitizesAndPaginates(t *testing.T) {
	dir := t.TempDir()
	writeLog(t, filepath.Join(dir, "api.jsonl"), "2026-08-12T00:00:00Z", "INFO", "GET https://secret.example/file")
	writeLog(t, filepath.Join(dir, "api.jsonl"), "2026-08-12T00:01:00Z", "ERROR", "first error")
	writeLog(t, filepath.Join(dir, "api.jsonl"), "2026-08-12T00:02:00Z", "ERROR", "second error")
	since := time.Date(2026, 8, 12, 0, 0, 30, 0, time.UTC)
	first, err := Read(context.Background(), dir, Query{Source: "api", Level: "error", Since: since, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Records) != 1 || first.Records[0].Message != "first error" || first.NextCursor == "" {
		t.Fatalf("unexpected first page: %#v", first)
	}
	second, err := Read(context.Background(), dir, Query{Source: "api", Level: "ERROR", Since: since, Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Records) != 1 || second.Records[0].Message != "second error" {
		t.Fatalf("unexpected second page: %#v", second)
	}

	unfiltered, err := Read(context.Background(), dir, Query{Source: "api", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unfiltered.Records[0].Message, "secret.example") || !strings.Contains(unfiltered.Records[0].Message, "[redacted]") {
		t.Fatalf("record was not sanitized: %#v", unfiltered.Records[0])
	}
}

func writeLog(t *testing.T, path, timestamp, level, message string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	b, err := json.Marshal(map[string]string{"time": timestamp, "level": level, "msg": message})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestReadReportsUnreadableSource(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "clamav.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := Read(context.Background(), dir, Query{Source: "clamav"})
	if !errors.Is(err, ErrLogUnreadable) {
		t.Fatalf("expected unreadable error, got %v", err)
	}
}
