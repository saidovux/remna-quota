// Package aggregate combines existing subscriptions without managing their
// users, quotas, renewals or ownership in an external application.
package aggregate

import (
	"context"
	"errors"
	"time"
)

const MaxSources = 16

var (
	ErrNotFound    = errors.New("bundle or source not found")
	ErrInvalid     = errors.New("invalid bundle request")
	ErrRevision    = errors.New("bundle revision conflict")
	ErrUnavailable = errors.New("subscription source unavailable")
	ErrAccess      = errors.New("subscription source access denied")
)

// Provider intentionally has no write operations. A source reference is an
// opaque identifier understood by a configured provider, never a fetched URL.
type Provider interface {
	Read(context.Context, string) (Snapshot, error)
	Links(context.Context, string) ([]string, error)
}

type ReferenceValidator interface{ ValidateReference(string) error }

type Snapshot struct {
	ID         string    `json:"id"`
	Username   string    `json:"username"`
	Status     string    `json:"status"`
	UsedBytes  int64     `json:"used_bytes"`
	LimitBytes int64     `json:"limit_bytes"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type SourceInput struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Provider  string `json:"provider"`
	Reference string `json:"reference"`
	Enabled   bool   `json:"enabled"`
}

type Source struct {
	SourceInput
	Snapshot  *Snapshot  `json:"snapshot,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

type PutRequest struct {
	Name             string            `json:"name"`
	ExternalRef      string            `json:"external_ref"`
	Metadata         map[string]string `json:"metadata"`
	Enabled          bool              `json:"enabled"`
	Sources          []SourceInput     `json:"sources"`
	ExpectedRevision *int64            `json:"expected_revision,omitempty"`
}

type PatchRequest struct {
	Name             *string            `json:"name,omitempty"`
	ExternalRef      *string            `json:"external_ref,omitempty"`
	Metadata         *map[string]string `json:"metadata,omitempty"`
	Enabled          *bool              `json:"enabled,omitempty"`
	ExpectedRevision *int64             `json:"expected_revision,omitempty"`
}

type Bundle struct {
	ObservationSequence uint64            `json:"observation_sequence,omitempty"`
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	ExternalRef         string            `json:"external_ref"`
	Metadata            map[string]string `json:"metadata"`
	Enabled             bool              `json:"enabled"`
	Sources             []Source          `json:"sources"`
	Revision            int64             `json:"revision"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	Token               string            `json:"-"`
	TokenHash           string            `json:"-"`
}

type ListOptions struct {
	Limit       int
	After       string
	ExternalRef string
}

// Update serializes a bundle, including missing rows, across all replicas.
// Its callback runs once and MUST NOT make network requests.
type Repository interface {
	Update(context.Context, string, func(*Bundle) error) (Bundle, error)
	Get(context.Context, string) (Bundle, error)
	ByTokenHash(context.Context, string) (Bundle, error)
	List(context.Context, ListOptions) ([]Bundle, error)
	Delete(context.Context, string, *int64) error
	Ping(context.Context) error
}
