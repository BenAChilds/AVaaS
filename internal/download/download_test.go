package download

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestPublicAddressPolicy(t *testing.T) {
	blockedIPs := []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "::1", "fd00::1", "2001:db8::1"}
	for _, raw := range blockedIPs {
		if isPublic(netip.MustParseAddr(raw)) {
			t.Errorf("%s should be blocked", raw)
		}
	}
	if !isPublic(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public address blocked")
	}
}
func TestFetchBlocksLoopback(t *testing.T) {
	d, err := New(t.TempDir(), 1024, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Fetch(context.Background(), "id", "http://127.0.0.1/file")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected blocked destination, got %v", err)
	}
}
