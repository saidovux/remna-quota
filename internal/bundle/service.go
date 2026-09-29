package bundle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	accountIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
	usernamePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{2,33}$`)
	partKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,9}$`)
)

// Service coordinates durable desired state and independently enforced parts.
// Desired-state transactions remain short; provider work uses a separate lock.
type Service struct {
	repo      Repository
	providers map[string]Provider
	now       func() time.Time
	runner    *runner
}

func NewService(repo Repository, providers map[string]Provider) *Service {
	copyProviders := make(map[string]Provider, len(providers))
	for name, provider := range providers {
		copyProviders[name] = provider
	}
	return &Service{repo: repo, providers: copyProviders, now: time.Now, runner: newRunner()}
}

// Put persists intent before contacting providers. A partial provider failure is
// represented on its part, while successful parts remain available for retry.
func (s *Service) Put(ctx context.Context, id string, req PutRequest) (Bundle, error) {
	req.Parts = append([]PartRequest(nil), req.Parts...)
	if err := s.validate(id, &req); err != nil {
		return Bundle{}, err
	}
	_, err := s.repo.Update(ctx, id, func(b *Bundle) error {
		if req.ExpectedRevision != nil && *req.ExpectedRevision != b.Revision {
			return ErrRevision
		}
		if req.DeviceLimit != nil && *req.DeviceLimit > 0 && *req.DeviceLimit < len(b.Devices) {
			return fmt.Errorf("%w: remove devices before lowering device_limit", ErrConflict)
		}
		now := s.now().UTC()
		if b.ID == "" {
			token, hash, err := newToken()
			if err != nil {
				return err
			}
			*b = Bundle{ID: id, Username: req.Username, Name: req.Name, ExternalRef: req.ExternalRef, Enabled: req.Enabled, Token: token, TokenHash: hash, Revision: 1, CreatedAt: now, UpdatedAt: now}
			b.DeviceLimit = req.DeviceLimit
			b.RenewalParts = renewalPartsFrom(req.Parts)
			if req.RenewalPriceMinor != nil && *req.RenewalPriceMinor >= 0 {
				b.RenewalPriceMinor = *req.RenewalPriceMinor
			}
			if req.RenewalPeriodDays != nil && *req.RenewalPeriodDays > 0 {
				b.RenewalPeriodDays = *req.RenewalPeriodDays
			}
			for _, part := range req.Parts {
				b.Parts = append(b.Parts, Part{PartRequest: part, Username: req.Username + "_" + part.Key, SyncStatus: "pending"})
			}
			return nil
		}
		if b.Username != req.Username {
			return fmt.Errorf("%w: username is immutable", ErrConflict)
		}
		existing := make(map[string]Part, len(b.Parts))
		for _, part := range b.Parts {
			existing[part.Key] = part
		}
		changed := b.Enabled != req.Enabled || len(b.Parts) != len(req.Parts) || b.Name != req.Name || b.ExternalRef != req.ExternalRef
		if req.DeviceLimit != nil && (b.DeviceLimit == nil || *b.DeviceLimit != *req.DeviceLimit) {
			b.DeviceLimit = req.DeviceLimit
			changed = true
		}
		parts := make([]Part, 0, len(req.Parts))
		for i, requested := range req.Parts {
			part, found := existing[requested.Key]
			if found {
				if part.Provider != requested.Provider || part.Profile != requested.Profile {
					return fmt.Errorf("%w: part provider and profile are immutable", ErrConflict)
				}
				delete(existing, requested.Key)
			} else {
				part = Part{Username: req.Username + "_" + requested.Key, SyncStatus: "pending"}
			}
			if !found || part.PartRequest != requested || b.Enabled != req.Enabled {
				part.SyncStatus, part.LastError = "pending", ""
				changed = true
			}
			if i >= len(b.Parts) || b.Parts[i].Key != requested.Key {
				changed = true
			}
			part.PartRequest = requested
			parts = append(parts, part)
		}
		if len(existing) != 0 {
			return fmt.Errorf("%w: existing parts must be retained; disable a part to retire it", ErrConflict)
		}
		if err := s.validateDeviceLimit(b.DeviceLimit, parts); err != nil {
			return err
		}
		if changed {
			b.Name, b.ExternalRef = req.Name, req.ExternalRef
			b.Parts, b.Enabled, b.UpdatedAt = parts, req.Enabled, now
			// Renewal mirrors the latest desired composition ("renew = same as now").
			b.RenewalParts = renewalPartsFrom(req.Parts)
			if req.RenewalPriceMinor != nil && *req.RenewalPriceMinor >= 0 {
				b.RenewalPriceMinor = *req.RenewalPriceMinor
			}
			if req.RenewalPeriodDays != nil && *req.RenewalPeriodDays > 0 {
				b.RenewalPeriodDays = *req.RenewalPeriodDays
			}
			b.Revision++
			markPending(b)
			b.NextSyncAt, b.NextRetryAt, b.FailureCount = s.now().UTC(), nil, 0
		}
		return nil
	})
	if err != nil {
		return Bundle{}, err
	}
	return s.Sync(ctx, id)
}

func (s *Service) validate(id string, req *PutRequest) error {
	invalid := func(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }
	if err := s.validateDeviceLimit(req.DeviceLimit, nil); err != nil {
		return err
	}
	if !accountIDPattern.MatchString(id) {
		return invalid("id must contain 1..80 ASCII letters, digits, underscores or hyphens")
	}
	if !validDisplay(req.Name) || !validDisplay(req.ExternalRef) || (req.ExpectedRevision != nil && *req.ExpectedRevision < 0) {
		return invalid("invalid name, external reference or revision")
	}
	if !usernamePattern.MatchString(req.Username) {
		return invalid("username must start with a lowercase letter and contain 3..34 lowercase letters, digits or underscores")
	}
	if len(req.Parts) < 1 || len(req.Parts) > 8 {
		return invalid("parts must contain 1..8 entries")
	}
	keys := make(map[string]bool, len(req.Parts))
	for i := range req.Parts {
		part := &req.Parts[i]
		if err := s.validateDeviceLimit(req.DeviceLimit, []Part{{PartRequest: *part}}); err != nil {
			return err
		}
		if !partKeyPattern.MatchString(part.Key) || keys[part.Key] {
			return invalid("part keys must be unique and contain 1..10 lowercase letters, digits or underscores, starting with a letter")
		}
		keys[part.Key] = true
		if len(req.Username)+1+len(part.Key) > 36 {
			return invalid("username plus underscore and part key must not exceed 36 characters")
		}
		if provider, exists := s.providers[part.Provider]; !exists || provider == nil {
			return invalid("unknown provider")
		}
		if len(part.Profile) == 0 || len(part.Profile) > 80 || strings.TrimSpace(part.Profile) != part.Profile {
			return invalid("part profile must contain 1..80 characters without surrounding whitespace")
		}
		part.Label = strings.TrimSpace(part.Label)
		if part.Label == "" {
			part.Label = part.Key
		}
		if !utf8.ValidString(part.Label) || utf8.RuneCountInString(part.Label) > 80 || strings.IndexFunc(part.Label, unicode.IsControl) >= 0 {
			return invalid("part label must contain at most 80 characters without control characters")
		}
		if part.LimitBytes < 0 {
			return invalid("limit_bytes cannot be negative")
		}
		if part.ExpiresAt.IsZero() || part.ExpiresAt.Year() < 1970 || part.ExpiresAt.Year() > 9999 {
			return invalid("expires_at must be an explicit timestamp")
		}
		part.ExpiresAt = part.ExpiresAt.Round(0).UTC()
		if part.ResetStrategy == "" {
			part.ResetStrategy = "NO_RESET"
		}
		switch part.ResetStrategy {
		case "NO_RESET", "DAY", "WEEK", "MONTH", "MONTH_ROLLING":
		default:
			return invalid("unsupported reset_strategy")
		}
		if validator, ok := s.providers[part.Provider].(Validator); ok {
			if err := validator.Validate(*part); err != nil {
				return invalid("unsupported provider profile or settings")
			}
		}
	}
	return nil
}

func (s *Service) Get(ctx context.Context, id string, refresh bool) (Bundle, error) {
	if !refresh {
		return s.repo.Get(ctx, id)
	}
	return s.refreshAccount(ctx, id)
}

func (s *Service) ByToken(ctx context.Context, token string) (Bundle, error) {
	if len(token) != 43 {
		return Bundle{}, ErrNotFound
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != token {
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

func (s *Service) RotateToken(ctx context.Context, id string) (Bundle, error) {
	return s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		token, hash, err := newToken()
		if err != nil {
			return err
		}
		b.Token, b.TokenHash, b.UpdatedAt = token, hash, s.now().UTC()
		for i := range b.Parts {
			if b.Parts[i].AppliedRevision == b.Revision {
				b.Parts[i].AppliedRevision++
			}
		}
		b.Revision++
		b.NextSyncAt, b.NextRetryAt, b.FailureCount = s.now().UTC(), nil, 0
		return nil
	})
}

func newToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate subscription token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(hash[:]), nil
}

func validDisplay(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= 128 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func (s *Service) List(ctx context.Context, opts ListOptions) ([]Bundle, error) {
	if opts.Limit < 1 || opts.Limit > 101 || (opts.After != "" && !accountIDPattern.MatchString(opts.After)) || !validDisplay(opts.ExternalRef) || len(opts.Search) > 34 || strings.IndexFunc(opts.Search, unicode.IsControl) >= 0 {
		return nil, ErrInvalid
	}
	return s.repo.List(ctx, opts)
}

func (s *Service) ProviderCatalog() []ProviderInfo {
	items := []ProviderInfo{}
	for id, p := range s.providers {
		profiles := []string{}
		if catalog, ok := p.(ProfileCatalog); ok {
			profiles = catalog.Profiles()
		}
		_, devices := p.(DeviceProvider)
		items = append(items, ProviderInfo{ID: id, Profiles: profiles, SharedDeviceLimit: devices})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func (s *Service) Patch(ctx context.Context, id string, req PatchRequest) (Bundle, error) {
	if err := s.validateDeviceLimit(req.DeviceLimit, nil); err != nil {
		return Bundle{}, err
	}
	if !accountIDPattern.MatchString(id) || (req.Name != nil && !validDisplay(*req.Name)) || (req.ExternalRef != nil && !validDisplay(*req.ExternalRef)) || (req.ExpectedRevision != nil && *req.ExpectedRevision < 0) {
		return Bundle{}, ErrInvalid
	}
	_, err := s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		if req.ExpectedRevision != nil && *req.ExpectedRevision != b.Revision {
			return ErrRevision
		}
		changed := false
		if req.DeviceLimit != nil {
			if err := s.validateDeviceLimit(req.DeviceLimit, b.Parts); err != nil {
				return err
			}
			if *req.DeviceLimit > 0 && *req.DeviceLimit < len(b.Devices) {
				return fmt.Errorf("%w: remove devices before lowering device_limit", ErrConflict)
			}
			if b.DeviceLimit == nil || *b.DeviceLimit != *req.DeviceLimit {
				b.DeviceLimit = req.DeviceLimit
				changed = true
			}
		}
		if req.Name != nil && b.Name != *req.Name {
			b.Name = *req.Name
			changed = true
		}
		if req.ExternalRef != nil && b.ExternalRef != *req.ExternalRef {
			b.ExternalRef = *req.ExternalRef
			changed = true
		}
		if req.Enabled != nil && b.Enabled != *req.Enabled {
			b.Enabled = *req.Enabled
			changed = true
			for i := range b.Parts {
				b.Parts[i].SyncStatus, b.Parts[i].LastError = "pending", ""
			}
		}
		if changed {
			b.Revision++
			markPending(b)
			b.NextSyncAt, b.NextRetryAt, b.FailureCount = s.now().UTC(), nil, 0
			b.UpdatedAt = s.now().UTC()
		}
		return nil
	})
	if err != nil {
		return Bundle{}, err
	}
	return s.Sync(ctx, id)
}

func (s *Service) PutPart(ctx context.Context, id string, requested PartRequest, expected *int64) (Bundle, error) {
	if !accountIDPattern.MatchString(id) || (expected != nil && *expected < 0) {
		return Bundle{}, ErrInvalid
	}
	_, err := s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		if expected != nil && *expected != b.Revision {
			return ErrRevision
		}
		req := PutRequest{Username: b.Username, Name: b.Name, ExternalRef: b.ExternalRef, Enabled: b.Enabled, ExpectedRevision: expected, Parts: []PartRequest{}}
		req.DeviceLimit = b.DeviceLimit
		found := -1
		for i, p := range b.Parts {
			if p.Key == requested.Key {
				if p.Provider != requested.Provider || p.Profile != requested.Profile {
					return ErrConflict
				}
				req.Parts = append(req.Parts, requested)
				found = i
			} else {
				req.Parts = append(req.Parts, p.PartRequest)
			}
		}
		if found < 0 {
			req.Parts = append(req.Parts, requested)
		}
		if err := s.validate(id, &req); err != nil {
			return err
		}
		if found < 0 {
			normalized := req.Parts[len(req.Parts)-1]
			b.Parts = append(b.Parts, Part{PartRequest: normalized, Username: b.Username + "_" + normalized.Key, SyncStatus: "pending"})
		} else if b.Parts[found].PartRequest != req.Parts[found] {
			b.Parts[found].PartRequest = req.Parts[found]
			b.Parts[found].SyncStatus, b.Parts[found].LastError = "pending", ""
		} else {
			return nil
		}
		b.Revision++
		markPending(b)
		b.NextSyncAt, b.NextRetryAt, b.FailureCount = s.now().UTC(), nil, 0
		b.UpdatedAt = s.now().UTC()
		return nil
	})
	if err != nil {
		return Bundle{}, err
	}
	return s.Sync(ctx, id)
}

func markPending(b *Bundle) {
	for i := range b.Parts {
		b.Parts[i].SyncStatus, b.Parts[i].LastError = "pending", ""
	}
}
