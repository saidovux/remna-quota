package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Bundle is an independent collection of existing subscriptions. ExternalRef
// is optional application metadata; several bundles may share it.
type Bundle struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	ExternalRef     string            `json:"external_ref"`
	Metadata        map[string]string `json:"metadata"`
	Enabled         bool              `json:"enabled"`
	SubscriptionURL string            `json:"subscription_url"`
	Sources         []Source          `json:"sources"`
	Status          string            `json:"status"`
	Stale           bool              `json:"stale"`
	Revision        int64             `json:"revision"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type SourceReference struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Provider  string `json:"provider"`
	Reference string `json:"reference"`
	Enabled   bool   `json:"enabled"`
}

type Source struct {
	SourceReference
	Username       string     `json:"username,omitempty"`
	Status         string     `json:"status"`
	UsedBytes      *int64     `json:"used_bytes"`
	LimitBytes     *int64     `json:"limit_bytes"`
	Unlimited      *bool      `json:"unlimited"`
	RemainingBytes *int64     `json:"remaining_bytes"`
	ExpiresAt      *time.Time `json:"expires_at"`
	Stale          bool       `json:"stale"`
	CheckedAt      *time.Time `json:"checked_at"`
	LastError      string     `json:"last_error,omitempty"`
}

type PutBundleRequest struct {
	Name             string            `json:"name"`
	ExternalRef      string            `json:"external_ref"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Enabled          bool              `json:"enabled"`
	Sources          []SourceReference `json:"sources"`
	ExpectedRevision *int64            `json:"expected_revision,omitempty"`
}

type PatchBundleRequest struct {
	Name             *string            `json:"name,omitempty"`
	ExternalRef      *string            `json:"external_ref,omitempty"`
	Metadata         *map[string]string `json:"metadata,omitempty"`
	Enabled          *bool              `json:"enabled,omitempty"`
	ExpectedRevision *int64             `json:"expected_revision,omitempty"`
}

type ListBundlesOptions struct {
	Limit       int
	Cursor      string
	ExternalRef string
}
type BundlePage struct {
	Items      []Bundle `json:"items"`
	NextCursor string   `json:"next_cursor"`
}
type ProviderCapability struct {
	ID            string `json:"id"`
	ReferenceType string `json:"reference_type"`
	Access        string `json:"access"`
}
type Capabilities struct {
	MaxParts               int                         `json:"max_parts"`
	ManagedProviders       []ManagedProviderCapability `json:"managed_providers"`
	APIVersion             string                      `json:"api_version"`
	Formats                []string                    `json:"formats"`
	MaxSources             int                         `json:"max_sources"`
	Providers              []ProviderCapability        `json:"providers"`
	ManagedAccountsEnabled bool                        `json:"managed_accounts_enabled"`
}

var sourceKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func bundlePath(id string) (string, error) {
	if !accountIDPattern.MatchString(id) || id == "resolve" {
		return "", ErrInvalidID
	}
	return "/api/v1/bundles/" + id, nil
}

func revisionParams(expected *int64) (url.Values, error) {
	q := url.Values{}
	if expected != nil {
		if *expected < 0 {
			return nil, ErrInvalidRequest
		}
		q.Set("expected_revision", strconv.FormatInt(*expected, 10))
	}
	return q, nil
}

func (c *Client) PutBundle(ctx context.Context, id string, wanted PutBundleRequest) (Bundle, error) {
	if wanted.Sources == nil {
		wanted.Sources = []SourceReference{}
	}
	return c.bundleRequest(ctx, http.MethodPut, id, "", nil, wanted)
}
func (c *Client) GetBundle(ctx context.Context, id string, refresh bool) (Bundle, error) {
	q := url.Values{}
	if refresh {
		q.Set("refresh", "true")
	}
	return c.bundleRequest(ctx, http.MethodGet, id, "", q, nil)
}
func (c *Client) PatchBundle(ctx context.Context, id string, patch PatchBundleRequest) (Bundle, error) {
	return c.bundleRequest(ctx, http.MethodPatch, id, "", nil, patch)
}
func (c *Client) RefreshBundle(ctx context.Context, id string) (Bundle, error) {
	return c.bundleRequest(ctx, http.MethodPost, id, "/refresh", nil, nil)
}
func (c *Client) RotateBundleToken(ctx context.Context, id string, expected *int64) (Bundle, error) {
	q, err := revisionParams(expected)
	if err != nil {
		return Bundle{}, err
	}
	return c.bundleRequest(ctx, http.MethodPost, id, "/rotate-token", q, nil)
}
func (c *Client) PutSource(ctx context.Context, id string, source SourceReference, expected *int64) (Bundle, error) {
	if !sourceKeyPattern.MatchString(source.Key) {
		return Bundle{}, ErrInvalidRequest
	}
	request := struct {
		SourceReference
		ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	}{source, expected}
	return c.bundleRequest(ctx, http.MethodPut, id, "/sources/"+source.Key, nil, request)
}
func (c *Client) RemoveSource(ctx context.Context, id, key string, expected *int64) (Bundle, error) {
	if !sourceKeyPattern.MatchString(key) {
		return Bundle{}, ErrInvalidRequest
	}
	q, err := revisionParams(expected)
	if err != nil {
		return Bundle{}, err
	}
	return c.bundleRequest(ctx, http.MethodDelete, id, "/sources/"+key, q, nil)
}
func (c *Client) DeleteBundle(ctx context.Context, id string, expected *int64) error {
	path, err := bundlePath(id)
	if err != nil {
		return err
	}
	q, err := revisionParams(expected)
	if err != nil {
		return err
	}
	return c.jsonRequest(ctx, http.MethodDelete, path, q, nil, nil)
}
func (c *Client) ResolveBundle(ctx context.Context, subscriptionURL string) (Bundle, error) {
	var b Bundle
	if subscriptionURL == "" || len(subscriptionURL) > 2048 {
		return b, ErrInvalidRequest
	}
	err := c.jsonRequest(ctx, http.MethodPost, "/api/v1/bundles/resolve", nil, map[string]string{"subscription_url": subscriptionURL}, &b)
	if err != nil {
		return b, err
	}
	return validateBundle(b, "")
}
func (c *Client) ListBundles(ctx context.Context, opts ListBundlesOptions) (BundlePage, error) {
	q := url.Values{}
	var page BundlePage
	if opts.Limit < 0 || opts.Limit > 100 {
		return page, ErrInvalidRequest
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if opts.ExternalRef != "" {
		q.Set("external_ref", opts.ExternalRef)
	}
	if err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/bundles", q, nil, &page); err != nil {
		return page, err
	}
	if page.Items == nil {
		return page, ErrInvalidResponse
	}
	for _, b := range page.Items {
		if _, err := validateBundle(b, ""); err != nil {
			return BundlePage{}, err
		}
	}
	return page, nil
}
func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	var caps Capabilities
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/capabilities", nil, nil, &caps)
	if err == nil && (caps.APIVersion != "1" || caps.MaxSources < 1 || (len(caps.Providers) == 0 && len(caps.ManagedProviders) == 0)) {
		err = ErrInvalidResponse
	}
	return caps, err
}

func (c *Client) bundleRequest(ctx context.Context, method, id, action string, q url.Values, body any) (Bundle, error) {
	var b Bundle
	path, err := bundlePath(id)
	if err != nil {
		return b, err
	}
	if err := c.jsonRequest(ctx, method, path+action, q, body, &b); err != nil {
		return b, err
	}
	return validateBundle(b, id)
}

func validateBundle(b Bundle, id string) (Bundle, error) {
	if !accountIDPattern.MatchString(b.ID) || (id != "" && b.ID != id) || b.Revision < 1 || b.SubscriptionURL == "" || b.Sources == nil {
		return Bundle{}, ErrInvalidResponse
	}
	switch b.Status {
	case "ready", "pending", "error", "disabled":
		return b, nil
	default:
		return Bundle{}, ErrInvalidResponse
	}
}

func (c *Client) jsonRequest(ctx context.Context, method, path string, q url.Values, body, dst any) error {
	u := *c.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = q.Encode()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return ErrInvalidRequest
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
	if err != nil {
		return ErrInvalidRequest
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "remna-quota-go/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return context.DeadlineExceeded
		}
		return ErrTransport
	}
	defer res.Body.Close()
	const maximum = 4 << 20
	data, err := io.ReadAll(io.LimitReader(res.Body, maximum+1))
	if err != nil || len(data) > maximum {
		return ErrInvalidResponse
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var message struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &message)
		switch message.Error {
		case "unauthorized", "not_found", "invalid_request", "invalid_json", "json_required", "body_too_large", "invalid_bundle", "invalid_account", "invalid_enabled", "revision_conflict", "source_unavailable", "invalid_revision", "invalid_cursor", "invalid_limit", "invalid_refresh", "invalid_subscription_url", "internal_error", "method_not_allowed", "diagnostics_unavailable":
		default:
			message.Error = "http_error"
		}
		return &APIError{StatusCode: res.StatusCode, Code: message.Error}
	}
	if dst == nil {
		if res.StatusCode != 204 {
			return ErrInvalidResponse
		}
		return nil
	}
	if res.StatusCode != 200 || json.Unmarshal(data, dst) != nil {
		return ErrInvalidResponse
	}
	return nil
}
