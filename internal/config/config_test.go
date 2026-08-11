package config

import (
	"reflect"
	"testing"
)

func TestDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxFileBytes != 2*1024*1024*1024 || c.WorkerConcurrency != 1 || c.ValkeyEnabled || len(c.DownloadAllowedPrivateHosts) != 0 {
		t.Fatalf("unexpected defaults: %#v", c)
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
