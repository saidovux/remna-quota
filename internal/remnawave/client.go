package remnawave

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/saidovux/remna-quota/internal/config"
)

const maxResponseBytes = 4 << 20

type Client struct {
	baseURL        *url.URL
	httpClient     *http.Client
	accessMode     string
	token          string
	caddyAPIKey    string
	forwardedFor   string
	forwardedProto string
}

func NewClient(cfg config.Remnawave) (*Client, error) {
	if cfg.BaseURL == nil {
		return nil, errors.New("Remnawave base URL is required")
	}
	if strings.TrimSpace(cfg.APIToken) == "" {
		return nil, errors.New("Remnawave API token is required")
	}
	return &Client{
		baseURL:        cfg.BaseURL,
		httpClient:     &http.Client{Timeout: cfg.RequestTimeout},
		accessMode:     cfg.AccessMode,
		token:          cfg.APIToken,
		caddyAPIKey:    cfg.CaddyAPIKey,
		forwardedFor:   cfg.ForwardedFor,
		forwardedProto: cfg.ForwardedProto,
	}, nil
}

func (c *Client) Metadata(ctx context.Context) (Metadata, error) {
	var envelope struct {
		Response Metadata `json:"response"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/system/metadata", nil, nil, http.StatusOK, &envelope); err != nil {
		return Metadata{}, err
	}
	if strings.TrimSpace(envelope.Response.Version) == "" {
		return Metadata{}, errors.New("Remnawave metadata response is missing version")
	}
	return envelope.Response, nil
}

func (c *Client) Health(ctx context.Context) error {
	var envelope struct {
		Response struct {
			RuntimeMetrics []json.RawMessage `json:"runtimeMetrics"`
		} `json:"response"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/system/health", nil, nil, http.StatusOK, &envelope); err != nil {
		return err
	}
	if envelope.Response.RuntimeMetrics == nil {
		return errors.New("Remnawave health response is missing runtimeMetrics")
	}
	return nil
}

func (c *Client) User(ctx context.Context, userID int64) (User, error) {
	if userID <= 0 {
		return User{}, errors.New("user ID must be positive")
	}
	return c.user(ctx, "/api/users/"+strconv.FormatInt(userID, 10))
}

func (c *Client) UserByShortUUID(ctx context.Context, shortUUID string) (User, error) {
	if strings.TrimSpace(shortUUID) == "" {
		return User{}, errors.New("short UUID is required")
	}
	return c.user(ctx, "/api/users/by-short-uuid/"+url.PathEscape(shortUUID))
}

func (c *Client) user(ctx context.Context, path string) (User, error) {
	var envelope struct {
		Response User `json:"response"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, nil, http.StatusOK, &envelope); err != nil {
		return User{}, err
	}
	if envelope.Response.ID <= 0 || envelope.Response.ShortUUID == "" || envelope.Response.Username == "" || envelope.Response.Status == "" {
		return User{}, errors.New("Remnawave user response is missing required identity fields")
	}
	return envelope.Response, nil
}

func (c *Client) InternalSquads(ctx context.Context) ([]InternalSquad, error) {
	var envelope struct {
		Response struct {
			Total  int64           `json:"total"`
			Squads []InternalSquad `json:"internalSquads"`
		} `json:"response"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/internal-squads", nil, nil, http.StatusOK, &envelope); err != nil {
		return nil, err
	}
	if envelope.Response.Squads == nil {
		return nil, errors.New("Remnawave squads response is missing internalSquads")
	}
	return envelope.Response.Squads, nil
}

func (c *Client) InternalSquad(ctx context.Context, uuid string) (InternalSquad, error) {
	var envelope struct {
		Response InternalSquad `json:"response"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/internal-squads/"+url.PathEscape(uuid), nil, nil, http.StatusOK, &envelope); err != nil {
		return InternalSquad{}, err
	}
	if envelope.Response.UUID == "" || envelope.Response.Name == "" {
		return InternalSquad{}, errors.New("Remnawave squad response is missing identity fields")
	}
	return envelope.Response, nil
}

func (c *Client) SquadAccessibleNodes(ctx context.Context, squadUUID string) ([]Node, error) {
	var envelope struct {
		Response struct {
			SquadUUID string `json:"squadUuid"`
			Nodes     []Node `json:"accessibleNodes"`
		} `json:"response"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/internal-squads/"+url.PathEscape(squadUUID)+"/accessible-nodes", nil, nil, http.StatusOK, &envelope); err != nil {
		return nil, err
	}
	if envelope.Response.SquadUUID == "" || envelope.Response.Nodes == nil {
		return nil, errors.New("Remnawave squad nodes response is missing required fields")
	}
	return envelope.Response.Nodes, nil
}

func (c *Client) UserAccessibleNodes(ctx context.Context, userID int64) ([]Node, error) {
	var envelope struct {
		Response struct {
			UserID int64  `json:"userId"`
			Nodes  []Node `json:"activeNodes"`
		} `json:"response"`
	}
	path := "/api/users/" + strconv.FormatInt(userID, 10) + "/accessible-nodes"
	if err := c.do(ctx, http.MethodGet, path, nil, nil, http.StatusOK, &envelope); err != nil {
		return nil, err
	}
	if envelope.Response.UserID != userID || envelope.Response.Nodes == nil {
		return nil, errors.New("Remnawave user nodes response is missing required fields")
	}
	return envelope.Response.Nodes, nil
}

func (c *Client) SquadUserDailyUsage(ctx context.Context, squadUUID string, userID int64, start, end time.Time) (DailyUsage, error) {
	if userID <= 0 {
		return DailyUsage{}, errors.New("user ID must be positive")
	}
	if end.Before(start) {
		return DailyUsage{}, errors.New("usage end is before start")
	}
	query := url.Values{"start": {start.UTC().Format(time.DateOnly)}, "end": {end.UTC().Format(time.DateOnly)}}
	path := "/api/bandwidth-stats/internal-squads/" + url.PathEscape(squadUUID) + "/users/" + strconv.FormatInt(userID, 10) + "/usage"
	var envelope struct {
		Response DailyUsage `json:"response"`
	}
	if err := c.do(ctx, http.MethodGet, path, query, nil, http.StatusOK, &envelope); err != nil {
		return DailyUsage{}, err
	}
	if envelope.Response.Days == nil {
		return DailyUsage{}, errors.New("Remnawave usage response is missing days")
	}
	return envelope.Response, nil
}

func (c *Client) AddUsersToSquad(ctx context.Context, squadUUID string, userIDs []int64) error {
	return c.mutateSquad(ctx, http.MethodPost, squadUUID, "add-many-users", userIDs)
}

func (c *Client) RemoveUsersFromSquad(ctx context.Context, squadUUID string, userIDs []int64) error {
	return c.mutateSquad(ctx, http.MethodDelete, squadUUID, "remove-many-users", userIDs)
}

func (c *Client) mutateSquad(ctx context.Context, method, squadUUID, action string, userIDs []int64) error {
	if len(userIDs) < 1 || len(userIDs) > 1000 {
		return errors.New("membership mutation requires 1..1000 user IDs")
	}
	for _, id := range userIDs {
		if id <= 0 {
			return errors.New("membership mutation user IDs must be positive")
		}
	}
	path := "/api/internal-squads/" + url.PathEscape(squadUUID) + "/bulk-actions/" + action
	return c.do(ctx, method, path, nil, struct {
		UserIDs []int64 `json:"userIds"`
	}{UserIDs: userIDs}, http.StatusAccepted, nil)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, requestBody any, expectedStatus int, dst any) error {
	correlationID := newCorrelationID()
	attempts := 1
	if method == http.MethodGet {
		attempts = 3
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			delay := retryDelay(attempt)
			if err := sleepContext(ctx, delay); err != nil {
				return err
			}
		}
		payload, err := marshalBody(requestBody)
		if err != nil {
			return err
		}
		u := *c.baseURL
		u.Path = strings.TrimRight(c.baseURL.Path, "/") + path
		u.RawQuery = query.Encode()
		req, err := http.NewRequestWithContext(ctx, method, u.String(), payload)
		if err != nil {
			return fmt.Errorf("build Remnawave request: %w", err)
		}
		c.setHeaders(req, correlationID, requestBody != nil)
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("Remnawave request failed (correlation %s): %w", correlationID, err)
			if method == http.MethodGet && retryableNetworkError(err) {
				continue
			}
			return lastErr
		}
		data, readErr := readBounded(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read Remnawave response (correlation %s): %w", correlationID, readErr)
		}
		if resp.StatusCode == expectedStatus {
			if dst == nil || len(data) == 0 {
				return nil
			}
			if err := json.Unmarshal(data, dst); err != nil {
				return fmt.Errorf("decode Remnawave response (correlation %s): %w", correlationID, err)
			}
			return nil
		}
		apiErr := decodeAPIError(resp.StatusCode, correlationID, data)
		lastErr = apiErr
		if method == http.MethodGet && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout) {
			continue
		}
		return apiErr
	}
	return lastErr
}

func (c *Client) setHeaders(req *http.Request, correlationID string, hasBody bool) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Request-ID", correlationID)
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.caddyAPIKey != "" {
		req.Header.Set("X-Api-Key", c.caddyAPIKey)
	}
	if c.accessMode == "docker_bridge" {
		req.Header.Set("X-Forwarded-For", c.forwardedFor)
		req.Header.Set("X-Forwarded-Proto", c.forwardedProto)
	}
}

func marshalBody(value any) (io.Reader, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode Remnawave request: %w", err)
	}
	return bytes.NewReader(data), nil
}

func readBounded(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	return data, nil
}

func decodeAPIError(status int, correlationID string, data []byte) error {
	var body struct {
		Message   string `json:"message"`
		ErrorCode string `json:"errorCode"`
	}
	_ = json.Unmarshal(data, &body)
	if body.Message == "" {
		body.Message = http.StatusText(status)
	}
	return &APIError{StatusCode: status, ErrorCode: body.ErrorCode, Message: body.Message, CorrelationID: correlationID}
}

func retryableNetworkError(err error) bool { var netErr net.Error; return errors.As(err, &netErr) }

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func newCorrelationID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(raw[:])
}

func retryDelay(attempt int) time.Duration {
	delay := time.Duration(100*(1<<(attempt-1))) * time.Millisecond
	var raw [1]byte
	if _, err := rand.Read(raw[:]); err == nil {
		delay += time.Duration(raw[0]%51) * time.Millisecond
	}
	return delay
}
