package notify

import (
	"reflect"
	"testing"

	"github.com/benachilds/avaas/internal/config"
	"github.com/valkey-io/valkey-go"
)

func TestClientOptionUsesLeastPrivilegeConnection(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.ValkeyHost = "valkey.example.test"
	cfg.ValkeyPort = 6380
	cfg.ValkeyUsername = "application"
	cfg.ValkeyDatabase = 2

	opt := clientOption(cfg, "secret")
	if !opt.DisableCache {
		t.Fatal("client-side caching must be disabled to avoid CLIENT TRACKING")
	}
	if !reflect.DeepEqual(opt.ClientSetInfo, valkey.DisableClientSetInfo) {
		t.Fatal("CLIENT SETINFO must be disabled for restricted ACL users")
	}
	if got, want := opt.InitAddress, []string{"valkey.example.test:6380"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("InitAddress = %v, want %v", got, want)
	}
	if opt.Username != "application" || opt.Password != "secret" || opt.SelectDB != 2 {
		t.Fatal("Valkey credentials or database were not preserved")
	}
}

func TestClientOptionConfiguresTLS(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.ValkeyTLS = true
	cfg.ValkeyTLSServerName = "valkey.example.test"
	opt := clientOption(cfg, "")
	if opt.TLSConfig == nil || opt.TLSConfig.ServerName != "valkey.example.test" {
		t.Fatal("Valkey TLS server name was not configured")
	}
}

func TestNewDoesNotConnectEagerly(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.ValkeyEnabled = true
	cfg.ValkeyHost = "192.0.2.1"
	cfg.ValkeyPort = 1
	cfg.ValkeyTopic = "avaas.test"

	publisher, err := New(cfg)
	if err != nil {
		t.Fatalf("New() attempted an eager connection: %v", err)
	}
	publisher.Close()
}
