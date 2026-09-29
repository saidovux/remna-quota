package providers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/saidovux/remna-quota/internal/aggregate"
	"github.com/saidovux/remna-quota/internal/bundle"
)

// RemnawaveReferences reads explicitly selected existing users. It does not
// adopt them or require the ownership marker of the optional managed adapter.
type RemnawaveReferences struct{ panel *Remnawave }

func NewRemnawaveReferences(cfg RemnaConfig) (*RemnawaveReferences, error) {
	p, err := NewRemnawave(cfg)
	if err != nil {
		return nil, err
	}
	return &RemnawaveReferences{panel: p}, nil
}

func (p *RemnawaveReferences) ValidateReference(id string) error {
	if !validID(id) {
		return aggregate.ErrInvalid
	}
	return nil
}

func referenceError(err error) error {
	switch {
	case errors.Is(err, bundle.ErrNotFound):
		return aggregate.ErrNotFound
	case errors.Is(err, bundle.ErrProviderAccess):
		return aggregate.ErrAccess
	default:
		return aggregate.ErrUnavailable
	}
}

func (p *RemnawaveReferences) Read(ctx context.Context, id string) (aggregate.Snapshot, error) {
	if p.ValidateReference(id) != nil {
		return aggregate.Snapshot{}, aggregate.ErrInvalid
	}
	u, err := p.panel.getUser(ctx, "/api/users/"+id)
	if err != nil {
		return aggregate.Snapshot{}, referenceError(err)
	}
	r := u.remote()
	if r.ID != id {
		return aggregate.Snapshot{}, aggregate.ErrUnavailable
	}
	return aggregate.Snapshot{ID: r.ID, Username: r.Username, Status: r.Status, UsedBytes: r.UsedBytes, LimitBytes: r.LimitBytes, ExpiresAt: r.ExpiresAt}, nil
}

func (p *RemnawaveReferences) Links(ctx context.Context, id string) ([]string, error) {
	r, err := p.Read(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Status != "ACTIVE" {
		return []string{}, nil
	}
	var envelope struct {
		Response struct {
			EnabledKeys []string `json:"enabledKeys"`
		} `json:"response"`
	}
	if err := p.panel.do(ctx, http.MethodGet, "/api/subscriptions/connection-keys/"+id, nil, http.StatusOK, &envelope); err != nil {
		return nil, referenceError(err)
	}
	if envelope.Response.EnabledKeys == nil {
		return nil, aggregate.ErrUnavailable
	}
	for _, link := range envelope.Response.EnabledKeys {
		if strings.ContainsAny(link, "\r\n") || !strings.Contains(link, "://") {
			return nil, aggregate.ErrUnavailable
		}
	}
	return envelope.Response.EnabledKeys, nil
}

// DemoReferences exposes two pre-existing fictional sources without provisioning.
type DemoReferences struct{ expires time.Time }

func NewDemoReferences() *DemoReferences {
	return &DemoReferences{expires: time.Now().UTC().Add(30 * 24 * time.Hour)}
}
func (p *DemoReferences) ValidateReference(id string) error {
	if id != "main" && id != "cdn" {
		return aggregate.ErrInvalid
	}
	return nil
}
func (p *DemoReferences) Read(ctx context.Context, id string) (aggregate.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return aggregate.Snapshot{}, err
	}
	if p.ValidateReference(id) != nil {
		return aggregate.Snapshot{}, aggregate.ErrNotFound
	}
	r := aggregate.Snapshot{ID: id, Username: "demo_" + id, Status: "ACTIVE", ExpiresAt: p.expires, UsedBytes: 1 << 30}
	if id == "cdn" {
		r.LimitBytes = 50 << 30
	}
	return r, nil
}
func (p *DemoReferences) Links(ctx context.Context, id string) ([]string, error) {
	if _, err := p.Read(ctx, id); err != nil {
		return nil, err
	}
	credential := "00000000-0000-4000-8000-000000000001"
	if id == "cdn" {
		credential = "00000000-0000-4000-8000-000000000002"
	}
	return []string{"vless://" + credential + "@demo.invalid:443?security=tls&type=tcp#DEMO-" + id}, nil
}

var _ aggregate.Provider = (*RemnawaveReferences)(nil)
var _ aggregate.Provider = (*DemoReferences)(nil)
