package logging

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var sensitive = regexp.MustCompile(`(?i)(https?://[^\s]+|authorization\s*[:=]\s*[^\s]+|avaas_[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|/var/spool/avaas/[^\s:]+)`)

func Sanitize(v string) string { return sensitive.ReplaceAllString(v, "[redacted]") }

type rotatingWriter struct {
	mu    sync.Mutex
	path  string
	max   int64
	files int
	f     *os.File
}

func newWriter(path string, max int64, files int) (*rotatingWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	w := &rotatingWriter{path: path, max: max, files: files}
	return w, w.open()
}
func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	w.f = f
	return err
}
func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if st, err := w.f.Stat(); err == nil && st.Size()+int64(len(p)) > w.max {
		_ = w.f.Close()
		for i := w.files - 1; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
		}
		_ = os.Rename(w.path, w.path+".1")
		if err = w.open(); err != nil {
			return 0, err
		}
	}
	return w.f.Write(p)
}

func New(process, dir string, max int64, files int) (*slog.Logger, error) {
	w, err := newWriter(filepath.Join(dir, process+".jsonl"), max, files)
	if err != nil {
		return nil, err
	}
	return slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stdout, w), &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Value.Kind() == slog.KindString {
			a.Value = slog.StringValue(Sanitize(a.Value.String()))
		}
		return a
	}})), nil
}

type Record struct {
	Timestamp time.Time `json:"timestamp"`
	Source    string    `json:"source"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}
type Query struct {
	Source, Level, Cursor string
	Since, Until          time.Time
	Limit                 int
}
type Result struct {
	Records    []Record `json:"records"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

var ErrCursorExpired = errors.New("cursor expired")

func Read(ctx context.Context, dir string, q Query) (Result, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	if q.Limit > 500 {
		q.Limit = 500
	}
	sources := []string{q.Source}
	if q.Source == "" || q.Source == "all" {
		sources = []string{"api", "worker", "clamav", "freshclam"}
	}
	allowed := map[string]bool{"api": true, "worker": true, "clamav": true, "freshclam": true}
	for _, s := range sources {
		if !allowed[s] {
			return Result{}, errors.New("invalid log source")
		}
	}
	var offset int64
	if q.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		if e != nil {
			return Result{}, ErrCursorExpired
		}
		offset, e = strconv.ParseInt(string(b), 10, 64)
		if e != nil {
			return Result{}, ErrCursorExpired
		}
	}
	var all []Record
	for _, source := range sources {
		paths, _ := filepath.Glob(filepath.Join(dir, source+".log*"))
		jsonPaths, _ := filepath.Glob(filepath.Join(dir, source+".jsonl*"))
		paths = append(paths, jsonPaths...)
		for _, p := range paths {
			recs, e := readFile(ctx, p, source, q)
			if e == nil {
				all = append(all, recs...)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Timestamp.Before(all[j].Timestamp) })
	if offset > int64(len(all)) {
		return Result{}, ErrCursorExpired
	}
	all = all[offset:]
	if len(all) > q.Limit {
		all = all[:q.Limit]
	}
	next := offset + int64(len(all))
	res := Result{Records: all}
	if len(all) == q.Limit {
		res.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(next, 10)))
	}
	return res, nil
}
func readFile(ctx context.Context, path, source string, q Query) ([]Record, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 1024*1024)
	var out []Record
	for s.Scan() {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		line := s.Text()
		r := parseRecord(line, source)
		if !q.Since.IsZero() && r.Timestamp.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && r.Timestamp.After(q.Until) {
			continue
		}
		if q.Level != "" && !strings.EqualFold(q.Level, r.Level) {
			continue
		}
		if strings.Contains(r.Message, "log retrieval audit") {
			continue
		}
		out = append(out, r)
	}
	return out, s.Err()
}
func parseRecord(line, source string) Record {
	r := Record{Timestamp: time.Now().UTC(), Source: source, Level: "INFO", Message: Sanitize(line)}
	var v map[string]any
	if json.Unmarshal([]byte(line), &v) == nil {
		if s, ok := v["time"].(string); ok {
			if t, e := time.Parse(time.RFC3339Nano, s); e == nil {
				r.Timestamp = t
			}
		}
		if s, ok := v["level"].(string); ok {
			r.Level = s
		}
		if s, ok := v["msg"].(string); ok {
			r.Message = Sanitize(s)
		}
	} else if len(line) > 19 {
		if t, e := time.Parse("Mon Jan _2 15:04:05 2006", line[:20]); e == nil {
			r.Timestamp = t
		}
	}
	return r
}
