package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxFileBytes != 2*1024*1024*1024 || c.WorkerConcurrency != 1 || c.ValkeyEnabled || len(c.DownloadAllowedPrivateHosts) != 0 {
		t.Fatalf("unexpected defaults: %#v", c)
	}
	if c.DefinitionUpdateTrigger != "/run/avaas/definition-update.request" || c.FreshclamPIDFile != "/run/avaas/freshclam.pid" || c.DefinitionUpdateMinInterval != 15*time.Minute {
		t.Fatalf("unexpected definition update defaults: %#v", c)
	}
}

func TestDefinitionUpdateOverrides(t *testing.T) {
	t.Setenv("AVAAS_DEFINITION_UPDATE_TRIGGER", "/run/clamav/request")
	t.Setenv("AVAAS_FRESHCLAM_PID_FILE", "/run/clamav/freshclam.pid")
	t.Setenv("AVAAS_DEFINITION_UPDATE_MIN_INTERVAL", "30m")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.DefinitionUpdateTrigger != "/run/clamav/request" || c.FreshclamPIDFile != "/run/clamav/freshclam.pid" || c.DefinitionUpdateMinInterval != 30*time.Minute {
		t.Fatalf("unexpected overrides: %#v", c)
	}
}

func TestRejectsUnsafeDefinitionUpdateConfiguration(t *testing.T) {
	for name, value := range map[string]string{"AVAAS_DEFINITION_UPDATE_TRIGGER": "/tmp/request", "AVAAS_FRESHCLAM_PID_FILE": "relative.pid", "AVAAS_DEFINITION_UPDATE_MIN_INTERVAL": "30s"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			_, err := Load("")
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("expected %s error, got %v", name, err)
			}
		})
	}
}

func TestAllowedPrivateHosts(t *testing.T) {
	t.Setenv("AVAAS_DOWNLOAD_ALLOWED_PRIVATE_HOSTS", " SIMSKIES.test.,10.0.0.5,simskies.test ")
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"simskies.test", "10.0.0.5"}
	if !reflect.DeepEqual(c.DownloadAllowedPrivateHosts, want) {
		t.Fatalf("allowed hosts = %#v, want %#v", c.DownloadAllowedPrivateHosts, want)
	}
}

func TestRejectsInvalidAllowedPrivateHosts(t *testing.T) {
	for _, value := range []string{"https://simskies.test", "*.simskies.test", "simskies_test", "simskies.test:443", ","} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AVAAS_DOWNLOAD_ALLOWED_PRIVATE_HOSTS", value)
			if _, err := Load(""); err == nil {
				t.Fatalf("accepted invalid host list %q", value)
			}
		})
	}
}
