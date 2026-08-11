package logging

import (
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
