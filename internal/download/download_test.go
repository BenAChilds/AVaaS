package download

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
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
	d, err := New(t.TempDir(), 1024, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Fetch(context.Background(), "id", "http://127.0.0.1/file")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("expected blocked destination, got %v", err)
	}
}

func TestPrivateAddressRequiresExactAllowedHost(t *testing.T) {
	allowed := map[string]struct{}{"simskies.test": {}}
	private := netip.MustParseAddr("192.168.65.254")
	if !addressAllowed("SIMSKIES.TEST.", private, allowed) {
		t.Fatal("exact allowlisted host should permit a private address")
	}
	for _, host := range []string{"other.test", "sub.simskies.test", ""} {
		if addressAllowed(host, private, allowed) {
			t.Fatalf("private address allowed for %q", host)
		}
	}
}

func TestAllowedHostCannotOverrideHardBlocks(t *testing.T) {
	allowed := map[string]struct{}{"simskies.test": {}}
	for _, raw := range []string{"127.0.0.1", "169.254.169.254", "100.100.100.200", "198.18.0.1", "::1", "fe80::1", "2001:db8::1"} {
		if addressAllowed("simskies.test", netip.MustParseAddr(raw), allowed) {
			t.Errorf("hard-blocked address %s was allowed", raw)
		}
	}
}

func TestAllowedHostIsResolvedAndRevalidatedForEveryConnection(t *testing.T) {
	allowed := map[string]struct{}{"simskies.test": {}}
	lookups := 0
	dials := 0
	lookup := func(_ context.Context, _, _ string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("192.168.65.254")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	}
	dial := func(context.Context, string, string) (net.Conn, error) {
		dials++
		return nil, nil
	}
	connect := destinationDialer(allowed, lookup, dial)
	if _, err := connect(context.Background(), "tcp", "simskies.test:443"); err != nil {
		t.Fatalf("first private resolution should be allowed: %v", err)
	}
	if _, err := connect(context.Background(), "tcp", "simskies.test:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("metadata rebinding was not blocked: %v", err)
	}
	if lookups != 2 || dials != 1 {
		t.Fatalf("lookups=%d dials=%d, want 2 and 1", lookups, dials)
	}
}

func TestAllowedPrivateHostFetchAndRedirectRevalidation(t *testing.T) {
	var redirectTarget string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" && redirectTarget != "" {
			http.Redirect(w, r, redirectTarget, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("clean payload"))
	}))
	t.Cleanup(server.Close)
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		switch strings.ToLower(host) {
		case "simskies.test":
			return []netip.Addr{netip.MustParseAddr("192.168.65.254")}, nil
		case "other.test":
			return []netip.Addr{netip.MustParseAddr("192.168.65.253")}, nil
		default:
			return nil, fmt.Errorf("unexpected host %q", host)
		}
	}
	dialer := &net.Dialer{}
	dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, serverURL.Host)
	}
	d, err := newWithNetwork(t.TempDir(), 1024, 2*time.Second, []string{"simskies.test"}, lookup, dial)
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Fetch(context.Background(), "allowed", "http://simskies.test:"+port+"/file")
	if err != nil || result.Bytes != int64(len("clean payload")) {
		t.Fatalf("allowed private fetch: result=%#v err=%v", result, err)
	}

	redirectTarget = "http://other.test:" + port + "/file"
	_, err = d.Fetch(context.Background(), "redirect", "http://simskies.test:"+port+"/redirect")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("redirect to non-allowlisted private host was not blocked: %v", err)
	}
}
