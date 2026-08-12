package definitions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ErrPending = errors.New("definition update already pending")

// Trigger atomically creates the request marker. An existing marker means the
// relay has already been notified and the request can be safely coalesced.
func Trigger(path string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if errors.Is(err, os.ErrExist) {
		return ErrPending
	}
	if err != nil {
		return err
	}
	return f.Close()
}

type Relay struct {
	TriggerPath string
	PIDFile     string
	MinInterval time.Duration
	Now         func() time.Time
	Signal      func(int, syscall.Signal) error
	ReadProcess func(int) (string, error)
}

func (r Relay) RunOnce() error {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Signal == nil {
		r.Signal = func(pid int, signal syscall.Signal) error { return syscall.Kill(pid, signal) }
	}
	if r.ReadProcess == nil {
		r.ReadProcess = readProcess
	}
	if _, err := os.Stat(r.TriggerPath); err != nil {
		return err
	}
	statePath := r.TriggerPath + ".last"
	if last, err := readLast(statePath); err == nil {
		if wait := r.MinInterval - r.Now().Sub(last); wait > 0 {
			return fmt.Errorf("definition update cooldown active for %s", wait.Round(time.Second))
		}
	}
	pid, err := readPID(r.PIDFile)
	if err != nil {
		return err
	}
	name, err := r.ReadProcess(pid)
	if err != nil {
		return fmt.Errorf("inspect FreshClam process: %w", err)
	}
	if !strings.Contains(strings.ToLower(name), "freshclam") {
		return fmt.Errorf("pid %d is not FreshClam", pid)
	}
	if err := r.Signal(pid, syscall.SIGUSR1); err != nil {
		return fmt.Errorf("signal FreshClam: %w", err)
	}
	if err := writeLast(statePath, r.Now().UTC()); err != nil {
		return err
	}
	return os.Remove(filepath.Clean(r.TriggerPath))
}

func (r Relay) Watch(stop <-chan struct{}) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(r.TriggerPath); err == nil {
			if err = r.RunOnce(); err != nil && !strings.Contains(err.Error(), "cooldown active") {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-stop:
			return nil
		case <-ticker.C:
		}
	}
}

func readPID(path string) (int, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return 0, fmt.Errorf("read FreshClam PID: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid < 2 {
		return 0, errors.New("FreshClam PID file is invalid")
	}
	return pid, nil
}

func readProcess(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	return strings.ReplaceAll(string(b), "\x00", " "), err
}

func readLast(path string) (time.Time, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, strings.TrimSpace(string(b)))
}

func writeLast(path string, value time.Time) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(filepath.Clean(tmp), []byte(value.Format(time.RFC3339Nano)+"\n"), 0o640); err != nil {
		return err
	}
	return os.Rename(filepath.Clean(tmp), filepath.Clean(path))
}
