package aggregate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/saidovux/remna-quota/internal/subscription"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

type Service struct {
	repo      Repository
	providers map[string]Provider
	now       func() time.Time
}

func NewService(repo Repository, providers map[string]Provider) *Service {
	return &Service{repo: repo, providers: maps.Clone(providers), now: time.Now}
}

func (s *Service) Providers() []string {
	names := make([]string, 0, len(s.providers))
	for name := range s.providers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func validText(v string, n int) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) <= n && strings.IndexFunc(v, unicode.IsControl) < 0
}

func (s *Service) validate(id string, r *PutRequest) error {
	if !idPattern.MatchString(id) || id == "resolve" || !validText(r.Name, 128) || !validText(r.ExternalRef, 128) || len(r.Metadata) > 16 || len(r.Sources) > MaxSources || (r.ExpectedRevision != nil && *r.ExpectedRevision < 0) {
		return ErrInvalid
	}
	if r.Metadata == nil {
		r.Metadata = map[string]string{}
	}
	for k, v := range r.Metadata {
		if !keyPattern.MatchString(k) || !validText(v, 256) {
			return ErrInvalid
		}
	}
	keys, refs := map[string]bool{}, map[string]bool{}
	for i := range r.Sources {
		p := &r.Sources[i]
		if p.Label == "" {
			p.Label = p.Key
		}
		provider, ok := s.providers[p.Provider]
		if !keyPattern.MatchString(p.Key) || keys[p.Key] || !ok || provider == nil || !validText(p.Label, 80) || p.Reference == "" || !validText(p.Reference, 256) || strings.TrimSpace(p.Reference) != p.Reference {
			return ErrInvalid
		}
		ref := p.Provider + "\x00" + p.Reference
		if refs[ref] {
			return ErrInvalid
		}
		keys[p.Key], refs[ref] = true, true
		if v, ok := provider.(ReferenceValidator); ok {
			if v.ValidateReference(p.Reference) != nil {
				return ErrInvalid
			}
		}
	}
	return nil
}

func newToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(hash[:]), nil
}

func (s *Service) apply(b *Bundle, id string, r PutRequest) error {
	r.Sources = slices.Clone(r.Sources)
	r.Metadata = maps.Clone(r.Metadata)
	if err := s.validate(id, &r); err != nil {
		return err
	}
	if r.ExpectedRevision != nil && *r.ExpectedRevision != b.Revision {
		return ErrRevision
	}
	if b.ID == "" {
		token, hash, err := newToken()
		if err != nil {
			return err
		}
		*b = Bundle{ID: id, Token: token, TokenHash: hash, CreatedAt: s.now().UTC()}
	}
	changed := b.Revision == 0 || b.Name != r.Name || b.ExternalRef != r.ExternalRef || b.Enabled != r.Enabled || !maps.Equal(b.Metadata, r.Metadata) || len(b.Sources) != len(r.Sources)
	existing := map[string]Source{}
	for _, source := range b.Sources {
		existing[source.Key] = source
	}
	sources := make([]Source, 0, len(r.Sources))
	for i, input := range r.Sources {
		previous, ok := existing[input.Key]
		if !ok || i >= len(b.Sources) || b.Sources[i].SourceInput != input {
			changed = true
		}
		if previous.Provider != input.Provider || previous.Reference != input.Reference {
			previous = Source{}
		}
		previous.SourceInput = input
		sources = append(sources, previous)
	}
	b.Name, b.ExternalRef, b.Metadata, b.Enabled, b.Sources = r.Name, r.ExternalRef, r.Metadata, r.Enabled, sources
	if changed {
		b.Revision++
		b.UpdatedAt = s.now().UTC()
	}
	return nil
}

func desired(b Bundle) PutRequest {
	r := PutRequest{Name: b.Name, ExternalRef: b.ExternalRef, Metadata: b.Metadata, Enabled: b.Enabled, Sources: make([]SourceInput, 0, len(b.Sources))}
	for _, p := range b.Sources {
		r.Sources = append(r.Sources, p.SourceInput)
	}
	return r
}

// Put persists references only. No provider call is necessary to compose a bundle.
func (s *Service) Put(ctx context.Context, id string, r PutRequest) (Bundle, error) {
	return s.repo.Update(ctx, id, func(b *Bundle) error { return s.apply(b, id, r) })
}

func (s *Service) Patch(ctx context.Context, id string, p PatchRequest) (Bundle, error) {
	return s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		r := desired(*b)
		r.ExpectedRevision = p.ExpectedRevision
		if p.Name != nil {
			r.Name = *p.Name
		}
		if p.ExternalRef != nil {
			r.ExternalRef = *p.ExternalRef
		}
		if p.Metadata != nil {
			r.Metadata = *p.Metadata
		}
		if p.Enabled != nil {
			r.Enabled = *p.Enabled
		}
		return s.apply(b, id, r)
	})
}

func (s *Service) PutSource(ctx context.Context, id string, source SourceInput, expected *int64) (Bundle, error) {
	return s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		r := desired(*b)
		r.ExpectedRevision = expected
		found := false
		for i := range r.Sources {
			if r.Sources[i].Key == source.Key {
				r.Sources[i] = source
				found = true
				break
			}
		}
		if !found {
			r.Sources = append(r.Sources, source)
		}
		return s.apply(b, id, r)
	})
}

func (s *Service) RemoveSource(ctx context.Context, id, key string, expected *int64) (Bundle, error) {
	if !keyPattern.MatchString(key) {
		return Bundle{}, ErrInvalid
	}
	return s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		r := desired(*b)
		r.ExpectedRevision = expected
		r.Sources = slices.DeleteFunc(r.Sources, func(p SourceInput) bool { return p.Key == key })
		return s.apply(b, id, r)
	})
}

func (s *Service) Get(ctx context.Context, id string, refresh bool) (Bundle, error) {
	if !idPattern.MatchString(id) {
		return Bundle{}, ErrInvalid
	}
	if !refresh {
		return s.repo.Get(ctx, id)
	}
	b, err := s.beginObservation(ctx, id, "")
	if err != nil {
		return Bundle{}, err
	}
	_, err = s.readAll(ctx, &b, false)
	// Provider failures are represented in the saved sources. Storage errors
	// must still reach the caller; do not report an unsaved snapshot as current.
	if err != nil && !errors.Is(err, ErrUnavailable) {
		return Bundle{}, err
	}
	return s.repo.Get(ctx, id)
}

func (s *Service) List(ctx context.Context, opts ListOptions) ([]Bundle, error) {
	if opts.Limit < 1 || opts.Limit > 101 || (opts.After != "" && !idPattern.MatchString(opts.After)) || !validText(opts.ExternalRef, 128) {
		return nil, ErrInvalid
	}
	return s.repo.List(ctx, opts)
}

func (s *Service) Delete(ctx context.Context, id string, expected *int64) error {
	if !idPattern.MatchString(id) || (expected != nil && *expected < 0) {
		return ErrInvalid
	}
	return s.repo.Delete(ctx, id, expected)
}

func (s *Service) RotateToken(ctx context.Context, id string, expected *int64) (Bundle, error) {
	return s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		if expected != nil && *expected != b.Revision {
			return ErrRevision
		}
		token, hash, err := newToken()
		if err != nil {
			return err
		}
		b.Token, b.TokenHash = token, hash
		b.Revision++
		b.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) ByToken(ctx context.Context, token string) (Bundle, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != token {
		return Bundle{}, ErrNotFound
	}
	hash := sha256.Sum256([]byte(token))
	b, err := s.repo.ByTokenHash(ctx, hex.EncodeToString(hash[:]))
	if err != nil {
		return Bundle{}, err
	}
	if !b.Enabled {
		return Bundle{}, ErrNotFound
	}
	return b, nil
}

func sourceError(err error) string {
	switch {
	case errors.Is(err, ErrNotFound):
		return "source_not_found"
	case errors.Is(err, ErrAccess):
		return "source_access_denied"
	default:
		return "source_unavailable"
	}
}

func (s *Service) read(ctx context.Context, p *Source, links bool) ([]string, error) {
	provider, ok := s.providers[p.Provider]
	if !ok {
		p.LastError = "provider_missing"
		return nil, ErrUnavailable
	}
	r, err := provider.Read(ctx, p.Reference)
	if err != nil {
		p.LastError = sourceError(err)
		return nil, ErrUnavailable
	}
	if r.ID != p.Reference || r.UsedBytes < 0 || r.LimitBytes < 0 || r.ExpiresAt.IsZero() || !slices.Contains([]string{"ACTIVE", "DISABLED", "EXPIRED", "LIMITED"}, r.Status) {
		p.LastError = "source_invalid_state"
		return nil, ErrUnavailable
	}
	now := s.now().UTC()
	p.Snapshot, p.CheckedAt, p.LastError = &r, &now, ""
	if !links || r.Status != "ACTIVE" || !r.ExpiresAt.After(now) || (r.LimitBytes > 0 && r.UsedBytes >= r.LimitBytes) {
		return nil, nil
	}
	raw, err := provider.Links(ctx, p.Reference)
	if err != nil {
		p.LastError = sourceError(err)
		return nil, ErrUnavailable
	}
	out := make([]string, 0, len(raw))
	for _, link := range raw {
		labeled, err := subscription.LabelLink(link, p.Label)
		if err != nil {
			p.LastError = "source_invalid_links"
			return nil, ErrUnavailable
		}
		out = append(out, labeled)
	}
	return out, nil
}

func (s *Service) readAll(ctx context.Context, b *Bundle, links bool) ([]string, error) {
	results := make([][]string, len(b.Sources))
	errs := make([]error, len(b.Sources))
	var wg sync.WaitGroup
	slots := make(chan struct{}, 4)
	for i := range b.Sources {
		if links && !b.Sources[i].Enabled {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() {
				if err := s.saveObservation(ctx, *b, b.Sources[i]); err != nil {
					errs[i] = err
				}
			}()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				errs[i] = ErrUnavailable
				b.Sources[i].LastError = "source_unavailable"
				return
			}
			results[i], errs[i] = s.read(ctx, &b.Sources[i], links)
		}(i)
	}
	wg.Wait()
	out := []string{}
	seen := map[string]bool{}
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	for _, result := range results {
		for _, link := range result {
			if !seen[link] {
				seen[link] = true
				out = append(out, link)
			}
		}
	}
	return out, nil
}

func (s *Service) Links(ctx context.Context, snapshot Bundle) ([]string, error) {
	b, err := s.beginObservation(ctx, snapshot.ID, snapshot.TokenHash)
	if err != nil {
		return nil, err
	}
	links, fetchErr := s.readAll(ctx, &b, true)
	current, err := s.repo.Get(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	if !current.Enabled || current.TokenHash != b.TokenHash {
		return nil, ErrNotFound
	}
	if current.Revision != b.Revision || current.ObservationSequence != b.ObservationSequence {
		return nil, ErrUnavailable
	}
	return links, fetchErr
}

func (s *Service) beginObservation(ctx context.Context, id, tokenHash string) (Bundle, error) {
	return s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" || (tokenHash != "" && (!b.Enabled || b.TokenHash != tokenHash)) {
			return ErrNotFound
		}
		b.ObservationSequence++
		return nil
	})
}

// A database sequence fences overlapping reads across replicas without holding
// a transaction during network I/O or relying on synchronized machine clocks.
func (s *Service) saveObservation(ctx context.Context, snapshot Bundle, source Source) error {
	_, err := s.repo.Update(ctx, snapshot.ID, func(b *Bundle) error {
		if b.ID == "" || b.TokenHash != snapshot.TokenHash {
			return ErrNotFound
		}
		if b.Revision != snapshot.Revision || b.ObservationSequence != snapshot.ObservationSequence {
			return nil
		}
		for i := range b.Sources {
			if b.Sources[i].Key == source.Key {
				b.Sources[i].Snapshot, b.Sources[i].CheckedAt, b.Sources[i].LastError = source.Snapshot, source.CheckedAt, source.LastError
				break
			}
		}
		return nil
	})
	return err
}

// RefreshAll is optional background observation, never remote reconciliation.
func (s *Service) RefreshAll(ctx context.Context) error {
	after := ""
	failed := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := s.List(ctx, ListOptions{Limit: 100, After: after})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Enabled {
				b, err := s.Get(ctx, row.ID, true)
				if err != nil && !errors.Is(err, ErrNotFound) {
					failed = true
				}
				for _, p := range b.Sources {
					if p.LastError != "" {
						failed = true
					}
				}
			}
			after = row.ID
		}
		if len(rows) < 100 {
			break
		}
	}
	if failed {
		return ErrUnavailable
	}
	return nil
}
