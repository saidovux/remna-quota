package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sync"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

// Demo is a process-local provider for exercising the API without a panel. Its
// links use the reserved .invalid domain and cannot provide VPN connectivity.
// State is rebuilt from desired bundles by reconciliation after a restart.
type Demo struct {
	mu     sync.Mutex
	users  map[string]bundle.Remote
	owners map[string]string
}

func NewDemo() *Demo {
	return &Demo{users: make(map[string]bundle.Remote), owners: make(map[string]string)}
}

func (p *Demo) Ensure(ctx context.Context, owner string, part bundle.Part) (bundle.Remote, error) {
	if err := ctx.Err(); err != nil {
		return bundle.Remote{}, err
	}
	if owner == "" || part.Key == "" || part.Username == "" || part.LimitBytes < 0 || part.ExpiresAt.IsZero() {
		return bundle.Remote{}, bundle.ErrInvalid
	}
	sum := sha256.Sum256([]byte(ownership(owner, part.Key)))
	id := "demo-" + hex.EncodeToString(sum[:12])
	p.mu.Lock()
	defer p.mu.Unlock()
	if part.Remote != nil && part.Remote.ID != "" && part.Remote.ID != id {
		return bundle.Remote{}, bundle.ErrConflict
	}
	if claimed, ok := p.owners[part.Username]; ok && claimed != id {
		return bundle.Remote{}, bundle.ErrConflict
	}
	r := p.users[id]
	r.ID = id
	r.Username = part.Username
	var err error
	part, err = bundle.EnforcementPart(part, r)
	if err != nil {
		return bundle.Remote{}, err
	}
	r.LimitBytes = part.LimitBytes
	r.ExpiresAt = part.ExpiresAt
	r.Status = "ACTIVE"
	if !part.Enabled {
		r.Status = "DISABLED"
	}
	r = effectiveRemote(r)
	p.users[id] = r
	p.owners[part.Username] = id
	return r, nil
}

func (p *Demo) Read(ctx context.Context, id string) (bundle.Remote, error) {
	if err := ctx.Err(); err != nil {
		return bundle.Remote{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.users[id]
	if !ok {
		return bundle.Remote{}, bundle.ErrNotFound
	}
	return effectiveRemote(r), nil
}

func (p *Demo) Links(ctx context.Context, id string) ([]string, error) {
	r, err := p.Read(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Status != "ACTIVE" || !r.ExpiresAt.After(time.Now()) {
		return []string{}, nil
	}
	// A deterministic credential helps tests detect that parts stay independent.
	sum := sha256.Sum256([]byte(id))
	raw := hex.EncodeToString(sum[:16])
	credential := raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:]
	return []string{"vless://" + credential + "@demo.invalid:443?security=tls&type=tcp#" + url.PathEscape("DEMO ONLY - "+r.Username)}, nil
}

var _ bundle.Provider = (*Demo)(nil)

func (p *Demo) Profiles() []string { return []string{"cdn", "main"} }
