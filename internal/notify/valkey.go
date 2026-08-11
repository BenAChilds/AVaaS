package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/benachilds/avaas/internal/config"
	"github.com/benachilds/avaas/internal/model"
	"github.com/valkey-io/valkey-go"
)

type Publisher interface {
	Publish(context.Context, model.Event) error
	Ping(context.Context) error
	Close()
}
type Disabled struct{}

func (Disabled) Publish(context.Context, model.Event) error { return nil }
func (Disabled) Ping(context.Context) error                 { return nil }
func (Disabled) Close()                                     {}

type Valkey struct {
	mu     sync.Mutex
	client valkey.Client
	option valkey.ClientOption
	topic  string
}

func New(c config.Config) (Publisher, error) {
	if !c.ValkeyEnabled {
		return Disabled{}, nil
	}
	password := ""
	if c.ValkeyPasswordFile != "" {
		b, err := os.ReadFile(c.ValkeyPasswordFile)
		if err != nil {
			return nil, err
		}
		password = strings.TrimSpace(string(b))
	}
	opt := clientOption(c, password)
	// Connections are deliberately lazy. Notification delivery is optional, so
	// an unavailable Valkey server must not prevent the scan worker from starting.
	return &Valkey{option: opt, topic: c.ValkeyTopic}, nil
}

func clientOption(c config.Config, password string) valkey.ClientOption {
	opt := valkey.ClientOption{
		InitAddress:  []string{fmt.Sprintf("%s:%d", c.ValkeyHost, c.ValkeyPort)},
		Username:     c.ValkeyUsername,
		Password:     password,
		SelectDB:     c.ValkeyDatabase,
		DisableCache: true,
		// AVaaS only publishes events and does not need CLIENT metadata commands.
		// Disabling them permits a least-privilege Valkey ACL containing only the
		// connection, PING, SELECT (when configured), PUBLISH, and topic grants.
		ClientSetInfo: valkey.DisableClientSetInfo,
	}
	if c.ValkeyTLS {
		opt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ValkeyTLSServerName}
	}
	return opt
}
func (v *Valkey) Publish(ctx context.Context, e model.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return v.do(ctx, func(client valkey.Client) error {
		return client.Do(ctx, client.B().Publish().Channel(v.topic).Message(string(b)).Build()).Error()
	})
}
func (v *Valkey) Ping(ctx context.Context) error {
	return v.do(ctx, func(client valkey.Client) error {
		return client.Do(ctx, client.B().Ping().Build()).Error()
	})
}
func (v *Valkey) do(ctx context.Context, operation func(valkey.Client) error) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.client == nil {
		client, err := valkey.NewClient(v.option)
		if err != nil {
			return err
		}
		v.client = client
	}
	if err := operation(v.client); err != nil {
		v.client.Close()
		v.client = nil
		return err
	}
	return nil
}
func (v *Valkey) Close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.client != nil {
		v.client.Close()
		v.client = nil
	}
}
