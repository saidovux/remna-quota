package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const maxResponseBytes = 1 << 20

var (
	ErrInvalidConfig   = errors.New("invalid remna-quota client configuration")
	ErrInvalidID       = errors.New("invalid remna-quota resource id")
	ErrInvalidRequest  = errors.New("invalid remna-quota request")
	ErrInvalidResponse = errors.New("invalid remna-quota response")
	ErrTransport       = errors.New("remna-quota transport failure")
	accountIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)
)

// APIError deliberately excludes upstream bodies and URLs so logging it does
// not disclose account credentials. Use errors.As to inspect StatusCode or Code.
type APIError struct {
	StatusCode int
	Code       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("remna-quota API: %s (HTTP %d)", e.Code, e.StatusCode)
}

type Client struct {
	baseURL    *url.URL
	apiKey     string
	httpClient *http.Client
}

type options struct {
	client  *http.Client
	timeout time.Duration
}
type Option func(*options) error

// WithHTTPClient copies the supplied client's transport and other settings.
// Redirects are always disabled and the timeout is controlled by WithTimeout.
func WithHTTPClient(client *http.Client) Option {
	return func(o *options) error {
		if client == nil {
			return ErrInvalidConfig
		}
		o.client = client
		return nil
	}
}

// WithTimeout sets the total HTTP request timeout. The default is 30 seconds.
func WithTimeout(timeout time.Duration) Option {
	return func(o *options) error {
		if timeout <= 0 {
			return ErrInvalidConfig
		}
		o.timeout = timeout
		return nil
	}
}

// New creates a concurrent-safe client. HTTP is allowed for private networks
// and local development. Use HTTPS when connecting over the public internet.
func New(baseURL, apiKey string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.TrimSpace(baseURL) != baseURL {
		return nil, ErrInvalidConfig
	}
	if len(apiKey) < 32 || strings.TrimSpace(apiKey) != apiKey || strings.IndexFunc(apiKey, unicode.IsControl) >= 0 {
		return nil, ErrInvalidConfig
	}
	o := options{client: http.DefaultClient, timeout: 30 * time.Second}
	for _, option := range opts {
		if option == nil {
			return nil, ErrInvalidConfig
		}
		if err := option(&o); err != nil {
			return nil, err
		}
	}
	httpClient := *o.client
	httpClient.Timeout = o.timeout
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: u, apiKey: apiKey, httpClient: &httpClient}, nil
}

func (c *Client) PutAccount(ctx context.Context, id string, request PutAccountRequest) (Account, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return Account{}, ErrInvalidRequest
	}
	return c.request(ctx, http.MethodPut, id, "", false, data)
}

// GetAccount reads stored state. With refresh=true it additionally reads current
// provider usage without changing limits, credentials, or traffic counters.
func (c *Client) GetAccount(ctx context.Context, id string, refresh bool) (Account, error) {
	return c.request(ctx, http.MethodGet, id, "", refresh, nil)
}

// SyncAccount retries applying desired settings without resetting usage.
func (c *Client) SyncAccount(ctx context.Context, id string) (Account, error) {
	return c.request(ctx, http.MethodPost, id, "sync", false, nil)
}

// RotateToken revokes the old combined subscription URL. Remote VPN credentials
// are preserved; users should replace the URL saved in their VPN client.
func (c *Client) RotateToken(ctx context.Context, id string) (Account, error) {
	return c.request(ctx, http.MethodPost, id, "rotate-token", false, nil)
}

// ResolveSubscription validates a combined URL through the trusted backend.
// Use it before creating a website session based on possession of that URL.
// The snapshot can be stale; GetAccount(refresh=true) reads current usage.
func (c *Client) ResolveSubscription(ctx context.Context, subscriptionURL string) (Account, error) {
	if len(subscriptionURL) == 0 || len(subscriptionURL) > 2048 {
		return Account{}, ErrInvalidRequest
	}
	body, err := json.Marshal(struct {
		URL string `json:"subscription_url"`
	}{subscriptionURL})
	if err != nil {
		return Account{}, ErrInvalidRequest
	}
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/subscriptions/resolve"
	endpoint.RawPath = ""
	return c.do(ctx, http.MethodPost, endpoint, "", body)
}

func (c *Client) request(ctx context.Context, method, id, action string, refresh bool, body []byte) (Account, error) {
	if !accountIDPattern.MatchString(id) {
		return Account{}, ErrInvalidID
	}
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/v1/accounts/" + id
	endpoint.RawPath = ""
	if action != "" {
		endpoint.Path += "/" + action
	}
	if refresh {
		endpoint.RawQuery = "refresh=true"
	}
	return c.do(ctx, method, endpoint, id, body)
}

func (c *Client) do(ctx context.Context, method string, endpoint url.URL, id string, body []byte) (Account, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return Account{}, ErrInvalidRequest
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "remna-quota-go/1")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Account{}, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return Account{}, context.DeadlineExceeded
		}
		return Account{}, ErrTransport
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return Account{}, ErrInvalidResponse
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		var message struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &message)
		code := message.Error
		switch code {
		case "unauthorized", "not_found", "not_ready", "invalid_refresh", "json_required", "invalid_json", "invalid_request", "invalid_subscription_url", "method_not_allowed", "unsupported_format", "body_too_large", "invalid_account", "account_conflict", "revision_conflict", "provider_unavailable", "internal_error":
		default:
			code = "http_error"
		}
		return Account{}, &APIError{StatusCode: response.StatusCode, Code: code}
	}
	var account Account
	if err := json.Unmarshal(data, &account); err != nil || (id != "" && account.ID != id) || !accountIDPattern.MatchString(account.ID) || account.Username == "" {
		return Account{}, ErrInvalidResponse
	}
	switch account.SyncStatus {
	case "ready", "pending", "error":
	default:
		return Account{}, ErrInvalidResponse
	}
	return account, nil
}
