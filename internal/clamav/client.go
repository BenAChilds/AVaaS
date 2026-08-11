package clamav

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"
)

type Client struct {
	Socket  string
	Timeout time.Duration
}
type Result struct {
	Infected  bool
	Signature string
	Raw       string
}

type VersionInfo struct {
	Raw               string     `json:"raw"`
	EngineVersion     string     `json:"engine_version,omitempty"`
	DefinitionVersion string     `json:"definition_version,omitempty"`
	DefinitionDate    *time.Time `json:"definition_date,omitempty"`
}

func ParseVersion(raw string) VersionInfo {
	info := VersionInfo{Raw: raw}
	parts := strings.Split(strings.TrimSpace(raw), "/")
	if len(parts) > 0 {
		info.EngineVersion = parts[0]
	}
	if len(parts) > 1 {
		info.DefinitionVersion = parts[1]
	}
	if len(parts) > 2 {
		if t, err := time.Parse("Mon Jan _2 15:04:05 2006", parts[2]); err == nil {
			u := t.UTC()
			info.DefinitionDate = &u
		}
	}
	return info
}

func (c Client) command(ctx context.Context, command string) (string, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "unix", filepath.Clean(c.Socket))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline := time.Now().Add(c.Timeout)
	if v, ok := ctx.Deadline(); ok && v.Before(deadline) {
		deadline = v
	}
	_ = conn.SetDeadline(deadline)
	if _, err = fmt.Fprintf(conn, "z%s\x00", command); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(conn).ReadString(0)
	return strings.TrimSuffix(line, "\x00"), err
}
func (c Client) Ping(ctx context.Context) error {
	v, err := c.command(ctx, "PING")
	if err != nil {
		return err
	}
	if strings.TrimSpace(v) != "PONG" {
		return fmt.Errorf("unexpected clamd response")
	}
	return nil
}
func (c Client) Version(ctx context.Context) (string, error) { return c.command(ctx, "VERSION") }
func (c Client) Scan(ctx context.Context, path string) (Result, error) {
	raw, err := c.command(ctx, "SCAN "+filepath.Clean(path))
	if err != nil {
		return Result{}, err
	}
	r := Result{Raw: raw}
	switch {
	case strings.HasSuffix(raw, " OK"):
		return r, nil
	case strings.HasSuffix(raw, " FOUND"):
		r.Infected = true
		part := strings.TrimSuffix(raw, " FOUND")
		if i := strings.LastIndex(part, ": "); i >= 0 {
			r.Signature = part[i+2:]
		}
		return r, nil
	case strings.Contains(raw, "ERROR"):
		return r, errors.New("clamd scan error")
	default:
		return r, errors.New("unrecognized clamd response")
	}
}
