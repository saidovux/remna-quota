package bundle

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryRepository is for tests and the explicit demo mode; it is not durable.
type MemoryRepository struct {
	mu         sync.Mutex
	rows       map[string]Bundle
	operations map[string]bool
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{rows: make(map[string]Bundle), operations: map[string]bool{}}
}

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
	for key, row := range r.rows {
		if key != id && (row.Username == b.Username || row.TokenHash == b.TokenHash) {
			return Bundle{}, ErrConflict
		}
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

func (r *MemoryRepository) Remove(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := r.rows[id]; !ok {
		return ErrNotFound
	}
	delete(r.rows, id)
	return nil
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

func (r *MemoryRepository) ListIDs(ctx context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(r.rows))
	for id := range r.rows {
		ids = append(ids, id)
	}
	return ids, nil
}

func (r *MemoryRepository) Ping(ctx context.Context) error { return ctx.Err() }

func (r *MemoryRepository) AcquireOperation(ctx context.Context, id string) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.operations[id] {
		return nil, ErrBusy
	}
	r.operations[id] = true
	return func() { r.mu.Lock(); delete(r.operations, id); r.mu.Unlock() }, nil
}

func (r *MemoryRepository) ListDue(ctx context.Context, now time.Time, after string, limit int) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids := []string{}
	for id, b := range r.rows {
		if id > after && !b.NextSyncAt.After(now) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (r *MemoryRepository) List(ctx context.Context, opts ListOptions) ([]Bundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items := []Bundle{}
	for _, b := range r.rows {
		if b.ID <= opts.After || (opts.ExternalRef != "" && b.ExternalRef != opts.ExternalRef) || (opts.Enabled != nil && b.Enabled != *opts.Enabled) || !strings.HasPrefix(b.Username, opts.Search) {
			continue
		}
		items = append(items, clone(b))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if len(items) > opts.Limit {
		items = items[:opts.Limit]
	}
	return items, nil
}
