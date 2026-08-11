package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrBlocked  = errors.New("destination is not public")
	ErrTooLarge = errors.New("file exceeds maximum size")
	ErrHTTP     = errors.New("remote HTTP error")
)

type HTTPError struct{ Status int }

func (e HTTPError) Error() string   { return fmt.Sprintf("remote returned HTTP %d", e.Status) }
func (e HTTPError) Temporary() bool { return e.Status == 408 || e.Status == 429 || e.Status >= 500 }

type Result struct {
	Path     string
	Bytes    int64
	Duration time.Duration
}
type Downloader struct {
	client *http.Client
	max    int64
	spool  string
}

func New(spool string, max int64, timeout time.Duration) (*Downloader, error) {
	if err := os.MkdirAll(spool, 0o755); err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, MaxIdleConns: 10, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, ExpectContinueTimeout: 2 * time.Second, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !isPublic(ip) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		return nil, ErrBlocked
	}}
	c := &http.Client{Transport: transport, Timeout: timeout}
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return ValidateURL(req.URL)
	}
	return &Downloader{client: c, max: max, spool: spool}, nil
}

func ValidateURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("only HTTP and HTTPS URLs with a host are supported")
	}
	if u.User != nil {
		return errors.New("URL user information is not supported")
	}
	return nil
}

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"), netip.MustParsePrefix("2001:db8::/32"),
}

func isPublic(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range blocked {
		if p.Contains(a) {
			return false
		}
	}
	return a.IsValid() && !a.IsLoopback() && !a.IsMulticast() && !a.IsPrivate() && !a.IsUnspecified()
}

func (d *Downloader) Fetch(ctx context.Context, id, raw string) (res Result, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return res, err
	}
	if err = ValidateURL(u); err != nil {
		return res, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return res, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "AVaaS/1")
	start := time.Now()
	resp, err := d.client.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return res, HTTPError{Status: resp.StatusCode}
	}
	if resp.ContentLength > d.max {
		return res, ErrTooLarge
	}
	path := filepath.Join(d.spool, id+".scan")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return res, err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	reader := io.LimitReader(resp.Body, d.max+1)
	n, err := io.Copy(f, reader)
	if err != nil {
		return res, err
	}
	if n > d.max {
		return res, ErrTooLarge
	}
	if err = f.Sync(); err != nil {
		return res, err
	}
	ok = true
	return Result{Path: path, Bytes: n, Duration: time.Since(start)}, nil
}

func Cleanup(spool string, olderThan time.Duration) error {
	entries, err := os.ReadDir(spool)
	if err != nil {
		return err
	}
	cut := time.Now().Add(-olderThan)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".scan") {
			continue
		}
		info, err := e.Info()
		if err == nil && info.ModTime().Before(cut) {
			_ = os.Remove(filepath.Join(spool, e.Name()))
		}
	}
	return nil
}
