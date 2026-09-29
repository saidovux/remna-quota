package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
)

type PatchAccountRequest struct {
	DeviceLimit      *int    `json:"device_limit,omitempty"`
	Name             *string `json:"name,omitempty"`
	ExternalRef      *string `json:"external_ref,omitempty"`
	Enabled          *bool   `json:"enabled,omitempty"`
	ExpectedRevision *int64  `json:"expected_revision,omitempty"`
}
type ListAccountsOptions struct {
	Limit                       int
	Cursor, Search, ExternalRef string
	Enabled                     *bool
}
type AccountPage struct {
	Items      []Account `json:"items"`
	NextCursor string    `json:"next_cursor"`
}
type ManagedProviderCapability struct {
	SharedDeviceLimit bool     `json:"shared_device_limit"`
	ID                string   `json:"id"`
	Profiles          []string `json:"profiles"`
}

var partKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,9}$`)

func (c *Client) ListAccounts(ctx context.Context, opts ListAccountsOptions) (AccountPage, error) {
	var page AccountPage
	if opts.Limit < 0 || opts.Limit > 100 {
		return page, ErrInvalidRequest
	}
	q := url.Values{}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Cursor != "" {
		q.Set("cursor", opts.Cursor)
	}
	if opts.Search != "" {
		q.Set("search", opts.Search)
	}
	if opts.ExternalRef != "" {
		q.Set("external_ref", opts.ExternalRef)
	}
	if opts.Enabled != nil {
		q.Set("enabled", strconv.FormatBool(*opts.Enabled))
	}
	if err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/accounts", q, nil, &page); err != nil {
		return page, err
	}
	if page.Items == nil {
		return page, ErrInvalidResponse
	}
	for _, a := range page.Items {
		if !accountIDPattern.MatchString(a.ID) || a.Username == "" || a.Revision < 1 || (a.SyncStatus != "ready" && a.SyncStatus != "pending" && a.SyncStatus != "error") {
			return AccountPage{}, ErrInvalidResponse
		}
	}
	return page, nil
}

func (c *Client) PatchAccount(ctx context.Context, id string, patch PatchAccountRequest) (Account, error) {
	data, err := json.Marshal(patch)
	if err != nil {
		return Account{}, ErrInvalidRequest
	}
	return c.request(ctx, http.MethodPatch, id, "", false, data)
}

// PutPart atomically adds or updates one part without replacing other settings.
// ExpectedRevision, when supplied, rejects a stale edit with revision_conflict.
func (c *Client) PutPart(ctx context.Context, id string, part PartRequest, expectedRevision *int64) (Account, error) {
	if !partKeyPattern.MatchString(part.Key) {
		return Account{}, ErrInvalidRequest
	}
	request := struct {
		PartRequest
		ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	}{part, expectedRevision}
	data, err := json.Marshal(request)
	if err != nil {
		return Account{}, ErrInvalidRequest
	}
	return c.request(ctx, http.MethodPut, id, "parts/"+part.Key, false, data)
}
