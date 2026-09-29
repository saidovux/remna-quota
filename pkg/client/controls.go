package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// AddBalance tops up (positive) or charges (negative) the account balance. The
// site uses it after a successful payment; auto-renew charges automatically.
// A non-empty idempotencyKey makes replays safe.
func (c *Client) AddBalance(ctx context.Context, id string, deltaMinor int64, idempotencyKey string) (Account, error) {
	data, err := json.Marshal(struct {
		DeltaMinor     int64  `json:"delta_minor"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
	}{deltaMinor, idempotencyKey})
	if err != nil {
		return Account{}, ErrInvalidRequest
	}
	return c.request(ctx, http.MethodPost, id, "balance", false, data)
}

// SetAutorenew enables or disables monthly automatic renewal from the balance.
func (c *Client) SetAutorenew(ctx context.Context, id string, enabled bool) (Account, error) {
	data, err := json.Marshal(struct {
		Enabled bool `json:"enabled"`
	}{enabled})
	if err != nil {
		return Account{}, ErrInvalidRequest
	}
	return c.request(ctx, http.MethodPost, id, "autorenew", false, data)
}

// AddPeriods queues count prepaid periods (1..60). They activate automatically
// when the current period ends. A non-empty idempotencyKey makes replays safe.
func (c *Client) AddPeriods(ctx context.Context, id string, count int, idempotencyKey string) (Account, error) {
	data, err := json.Marshal(struct {
		Count          int    `json:"count"`
		IdempotencyKey string `json:"idempotency_key,omitempty"`
	}{count, idempotencyKey})
	if err != nil {
		return Account{}, ErrInvalidRequest
	}
	return c.request(ctx, http.MethodPost, id, "periods", false, data)
}

// DeleteAccount permanently removes the account and its remote users. It is
// idempotent: a missing account is not an error (204 or 404 both succeed).
func (c *Client) DeleteAccount(ctx context.Context, id string) error {
	if !accountIDPattern.MatchString(id) {
		return ErrInvalidID
	}
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/accounts/" + id
	endpoint.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint.String(), nil)
	if err != nil {
		return ErrInvalidRequest
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "remna-quota-go/1")
	response, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrTransport
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
	switch response.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	default:
		return &APIError{StatusCode: response.StatusCode, Code: "http_error"}
	}
}
