package updatecheck

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStableChannelIgnoresBetas(t *testing.T) {
	checker := testChecker(t, "v0.5.2", `[
        {"tag_name":"v0.6.0b","html_url":"https://github.com/BenAChilds/AVaaS/releases/tag/v0.6.0b","prerelease":true},
        {"tag_name":"v0.5.3","html_url":"https://github.com/BenAChilds/AVaaS/releases/tag/v0.5.3","prerelease":false}
    ]`)
	checker.Check(context.Background())
	got := checker.Snapshot()
	if got.Status != "available" || !got.Available || got.LatestVersion != "v0.5.3" || got.Channel != "stable" {
		t.Fatalf("unexpected status: %#v", got)
	}
}

func TestBetaChannelSeesBetaAndStable(t *testing.T) {
	checker := testChecker(t, "v0.5.2b", `[
        {"tag_name":"v0.5.3b","html_url":"https://example.test/beta","prerelease":true},
        {"tag_name":"v0.5.2","html_url":"https://example.test/stable","prerelease":false},
        {"tag_name":"v9.0.0","draft":true,"prerelease":false}
    ]`)
	checker.Check(context.Background())
	got := checker.Snapshot()
	if got.LatestVersion != "v0.5.3b" || got.Channel != "beta" || !got.Available {
		t.Fatalf("unexpected status: %#v", got)
	}
}

func TestDevelopmentAndDisabledStatuses(t *testing.T) {
	development := New(true, time.Hour, "dev", nil, slog.Default()).Snapshot()
	if development.Status != "unknown" || development.Channel != "development" {
		t.Fatalf("unexpected development status: %#v", development)
	}
	disabled := New(false, time.Hour, "v1.0.0", nil, slog.Default()).Snapshot()
	if disabled.Status != "disabled" || disabled.Channel != "stable" {
		t.Fatalf("unexpected disabled status: %#v", disabled)
	}
}

func TestCheckUsesETagAndRetainsSuccessfulResultOnFailure(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(http.StatusOK, `[{"tag_name":"v1.1.0","html_url":"https://example.test/release","prerelease":false}]`, `"release-etag"`), nil
		}
		if req.Header.Get("If-None-Match") != `"release-etag"` {
			t.Fatalf("missing ETag: %q", req.Header.Get("If-None-Match"))
		}
		return nil, errors.New("network unavailable")
	})}
	checker := New(true, time.Hour, "v1.0.0", client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	checker.Check(context.Background())
	want := checker.Snapshot()
	checker.Check(context.Background())
	got := checker.Snapshot()
	if got.Status != want.Status || got.LatestVersion != want.LatestVersion || got.CheckedAt == nil {
		t.Fatalf("successful result was not retained: %#v", got)
	}
}

func TestRejectsOversizedAndMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{"oversized": strings.Repeat("x", maxResponseBytes+1), "malformed": "{"} {
		t.Run(name, func(t *testing.T) {
			checker := testChecker(t, "v1.0.0", body)
			checker.Check(context.Background())
			if got := checker.Snapshot(); got.Status != "unknown" || got.CheckedAt != nil {
				t.Fatalf("unexpected status: %#v", got)
			}
		})
	}
}

func testChecker(t *testing.T, current, body string) *Checker {
	t.Helper()
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != releasesURL {
			t.Fatalf("unexpected URL %s", req.URL)
		}
		return response(http.StatusOK, body, ""), nil
	})}
	checker := New(true, time.Hour, current, client, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC)
	checker.now = func() time.Time { return now }
	return checker
}

func response(status int, body, etag string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Etag": []string{etag}}, Body: io.NopCloser(strings.NewReader(body))}
}
