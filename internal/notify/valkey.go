package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
	client valkey.Client
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
	opt := valkey.ClientOption{InitAddress: []string{fmt.Sprintf("%s:%d", c.ValkeyHost, c.ValkeyPort)}, Username: c.ValkeyUsername, Password: password, SelectDB: c.ValkeyDatabase}
	if c.ValkeyTLS {
		opt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ValkeyTLSServerName}
	}
	client, err := valkey.NewClient(opt)
	if err != nil {
		return nil, err
	}
	return &Valkey{client: client, topic: c.ValkeyTopic}, nil
}
func (v *Valkey) Publish(ctx context.Context, e model.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return v.client.Do(ctx, v.client.B().Publish().Channel(v.topic).Message(string(b)).Build()).Error()
}
func (v *Valkey) Ping(ctx context.Context) error {
	return v.client.Do(ctx, v.client.B().Ping().Build()).Error()
}
func (v *Valkey) Close() { v.client.Close() }
