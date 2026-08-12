package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

const releasesURL = "https://api.github.com/repos/BenAChilds/AVaaS/releases?per_page=30"
const maxResponseBytes = 1024 * 1024

type Status struct {
	CurrentVersion string     `json:"current_version"`
	Channel        string     `json:"channel"`
	Status         string     `json:"status"`
	Available      bool       `json:"available"`
	LatestVersion  string     `json:"latest_version,omitempty"`
	ReleaseURL     string     `json:"release_url,omitempty"`
	CheckedAt      *time.Time `json:"checked_at,omitempty"`
}

type Provider interface {
	Snapshot() Status
}

type Checker struct {
	enabled  bool
	interval time.Duration
	current  parsedVersion
	client   *http.Client
	log      *slog.Logger
	now      func() time.Time

	mu     sync.RWMutex
	status Status
	etag   string
}

type release struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type parsedVersion struct {
	raw                 string
	major, minor, patch int
	beta                bool
	valid               bool
}

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(b)?$`)

func New(enabled bool, interval time.Duration, current string, client *http.Client, log *slog.Logger) *Checker {
	v := parseVersion(current)
	status := Status{CurrentVersion: current, Channel: channel(v), Status: "unknown", Available: false}
	if !enabled {
		status.Status = "disabled"
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if req.URL.Scheme != "https" {
				return errors.New("release redirect must use HTTPS")
			}
			return nil
		}}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Checker{enabled: enabled, interval: interval, current: v, client: client, log: log, now: time.Now, status: status}
}

func (c *Checker) Snapshot() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

func (c *Checker) Start(ctx context.Context) {
	if !c.enabled || !c.current.valid {
		return
	}
	go func() {
		c.Check(ctx)
		ticker := time.NewTicker(c.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.Check(ctx)
			}
		}
	}()
}

func (c *Checker) Check(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "AVaaS-update-checker")
	c.mu.RLock()
	etag := c.etag
	c.mu.RUnlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.log.Warn("AVaaS release check failed", "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	now := c.now().UTC()
	if resp.StatusCode == http.StatusNotModified {
		c.mu.Lock()
		c.status.CheckedAt = &now
		c.mu.Unlock()
		return
	}
	if resp.StatusCode != http.StatusOK {
		c.log.Warn("AVaaS release check failed", "status", resp.StatusCode)
		return
	}
	limited := io.LimitReader(resp.Body, maxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil || len(body) > maxResponseBytes {
		c.log.Warn("AVaaS release response was unreadable or too large")
		return
	}
	var releases []release
	if err = json.Unmarshal(body, &releases); err != nil {
		c.log.Warn("AVaaS release response was invalid", "error", err)
		return
	}
	latest, ok := selectLatest(c.current, releases)
	status := Status{CurrentVersion: c.current.raw, Channel: channel(c.current), Status: "current", CheckedAt: &now}
	if ok {
		v := parseVersion(latest.TagName)
		status.LatestVersion = latest.TagName
		status.ReleaseURL = latest.HTMLURL
		status.Available = compare(v, c.current) > 0
		if status.Available {
			status.Status = "available"
		}
	}
	c.mu.Lock()
	c.status = status
	c.etag = resp.Header.Get("ETag")
	c.mu.Unlock()
}

func selectLatest(current parsedVersion, releases []release) (release, bool) {
	type candidate struct {
		release release
		version parsedVersion
	}
	var candidates []candidate
	for _, item := range releases {
		v := parseVersion(item.TagName)
		if item.Draft || !v.valid || (!current.beta && (item.Prerelease || v.beta)) {
			continue
		}
		if item.Prerelease != v.beta {
			continue
		}
		candidates = append(candidates, candidate{release: item, version: v})
	}
	sort.Slice(candidates, func(i, j int) bool { return compare(candidates[i].version, candidates[j].version) > 0 })
	if len(candidates) == 0 {
		return release{}, false
	}
	return candidates[0].release, true
}

func parseVersion(raw string) parsedVersion {
	v := parsedVersion{raw: raw}
	match := versionPattern.FindStringSubmatch(raw)
	if match == nil {
		return v
	}
	v.major, _ = strconv.Atoi(match[1])
	v.minor, _ = strconv.Atoi(match[2])
	v.patch, _ = strconv.Atoi(match[3])
	v.beta = match[4] == "b"
	v.valid = true
	return v
}

func channel(v parsedVersion) string {
	if !v.valid {
		return "development"
	}
	if v.beta {
		return "beta"
	}
	return "stable"
}

func compare(a, b parsedVersion) int {
	for _, pair := range [][2]int{{a.major, b.major}, {a.minor, b.minor}, {a.patch, b.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if a.beta == b.beta {
		return 0
	}
	if a.beta {
		return -1
	}
	return 1
}
