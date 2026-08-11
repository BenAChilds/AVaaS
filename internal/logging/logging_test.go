package logging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
