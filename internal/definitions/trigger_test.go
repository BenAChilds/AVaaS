package definitions

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTriggerCoalesces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.request")
	if err := Trigger(path); err != nil {
		t.Fatal(err)
	}
	if err := Trigger(path); !errors.Is(err, ErrPending) {
		t.Fatalf("expected pending, got %v", err)
	}
}

func TestRelayValidatesAndSignalsFreshClam(t *testing.T) {
	dir := t.TempDir()
	trigger := filepath.Join(dir, "update.request")
	pidFile := filepath.Join(dir, "freshclam.pid")
	if err := os.WriteFile(trigger, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidFile, []byte("42\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	called := false
	r := Relay{TriggerPath: trigger, PIDFile: pidFile, MinInterval: 15 * time.Minute, Now: func() time.Time { return now }, ReadProcess: func(pid int) (string, error) { return "/usr/bin/freshclam --daemon", nil }, Signal: func(pid int, signal syscall.Signal) error {
		called = pid == 42 && signal == syscall.SIGUSR1
		return nil
	}}
	if err := r.RunOnce(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("FreshClam was not signalled")
	}
	if _, err := os.Stat(trigger); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("trigger was not removed")
	}
}

func TestRelayRejectsWrongProcess(t *testing.T) {
	dir := t.TempDir()
	trigger := filepath.Join(dir, "update.request")
	pidFile := filepath.Join(dir, "freshclam.pid")
	_ = os.WriteFile(trigger, nil, 0o640)
	_ = os.WriteFile(pidFile, []byte("42\n"), 0o640)
	r := Relay{TriggerPath: trigger, PIDFile: pidFile, MinInterval: time.Minute, ReadProcess: func(int) (string, error) { return "/bin/other", nil }}
	if err := r.RunOnce(); err == nil {
		t.Fatal("expected process validation error")
	}
}
