package config

import "testing"

func TestDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxFileBytes != 2*1024*1024*1024 || c.WorkerConcurrency != 1 || c.ValkeyEnabled {
		t.Fatalf("unexpected defaults: %#v", c)
	}
}
