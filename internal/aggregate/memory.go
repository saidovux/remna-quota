package aggregate

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
)

type MemoryRepository struct {
	mu   sync.Mutex
	rows map[string]Bundle
}

func NewMemoryRepository() *MemoryRepository { return &MemoryRepository{rows: map[string]Bundle{}} }

func clone(b Bundle) Bundle {
	data, _ := json.Marshal(b)
	var out Bundle
	_ = json.Unmarshal(data, &out)
	out.Token, out.TokenHash = b.Token, b.TokenHash
	return out
}

func (r *MemoryRepository) Update(ctx context.Context, id string, fn func(*Bundle) error) (Bundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	b := clone(r.rows[id])
	if err := fn(&b); err != nil {
		return Bundle{}, err
	}
	if b.ID != id || b.Token == "" || b.TokenHash == "" {
		return Bundle{}, ErrInvalid
	}
	r.rows[id] = clone(b)
	return clone(b), nil
}

func (r *MemoryRepository) Get(ctx context.Context, id string) (Bundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	b, ok := r.rows[id]
	if !ok {
		return Bundle{}, ErrNotFound
	}
	return clone(b), nil
}

func (r *MemoryRepository) ByTokenHash(ctx context.Context, hash string) (Bundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	for _, b := range r.rows {
		if b.TokenHash == hash {
			return clone(b), nil
		}
	}
	return Bundle{}, ErrNotFound
}

func (r *MemoryRepository) List(ctx context.Context, opts ListOptions) ([]Bundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []Bundle{}
	for _, b := range r.rows {
		if b.ID > opts.After && (opts.ExternalRef == "" || opts.ExternalRef == b.ExternalRef) {
			out = append(out, clone(b))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func (r *MemoryRepository) Delete(ctx context.Context, id string, expected *int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	b, ok := r.rows[id]
	if !ok {
		return ErrNotFound
	}
	if expected != nil && *expected != b.Revision {
		return ErrRevision
	}
	delete(r.rows, id)
	return nil
}

func (r *MemoryRepository) Ping(ctx context.Context) error { return ctx.Err() }
