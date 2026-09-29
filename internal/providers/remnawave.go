// Package providers contains adapters for independently metered subscriptions.
package providers

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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

const maxResponseBytes = 4 << 20
const maxJSONInteger = int64(1<<53 - 1)

// AccessError retains only an HTTP status, never the upstream response or token.
type AccessError struct{ StatusCode int }

func (e *AccessError) Error() string {
	return fmt.Sprintf("Remnawave API access denied (HTTP %d)", e.StatusCode)
}
func (e *AccessError) Unwrap() []error {
	return []error{bundle.ErrUnavailable, bundle.ErrProviderAccess}
}

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,36}$`)
var squadPattern = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

// RemnaConfig targets the Remnawave v3 API. Profiles are administrator-controlled
// lists of internal squads; callers cannot inject arbitrary subscription URLs.
type RemnaConfig struct {
	HWIDExternalSquadUUID string
	BaseURL               string
	APIToken              string
	Profiles              map[string][]string
	Timeout               time.Duration
	AllowHTTP             bool
	CaddyAPIKey           string
	ForwardedFor          string
	ForwardedProto        string
}

type Remnawave struct {
	hwidExternalSquadUUID string
	baseURL               *url.URL
	client                *http.Client
	token                 string
	profiles              map[string][]string
	caddyAPIKey           string
	forwardedFor          string
	forwardedProto        string
}

func NewRemnawave(cfg RemnaConfig) (*Remnawave, error) {
	if cfg.HWIDExternalSquadUUID != "" && !squadPattern.MatchString(cfg.HWIDExternalSquadUUID) {
		return nil, errors.New("invalid HWID external squad UUID")
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && cfg.AllowHTTP)) {
		return nil, errors.New("Remnawave requires an HTTPS base URL (HTTP needs explicit allow_http)")
	}
	if strings.TrimSpace(cfg.APIToken) == "" || strings.ContainsAny(cfg.APIToken+cfg.CaddyAPIKey+cfg.ForwardedFor+cfg.ForwardedProto, "\r\n") {
		return nil, errors.New("Remnawave API credentials or forwarding headers are invalid")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Timeout < time.Second || cfg.Timeout > time.Minute {
		return nil, errors.New("Remnawave timeout must be between one second and one minute")
	}
	profiles := make(map[string][]string, len(cfg.Profiles))
	for name, squads := range cfg.Profiles {
		if strings.TrimSpace(name) == "" || len(squads) == 0 {
			return nil, errors.New("Remnawave profiles require a name and at least one squad")
		}
		for _, id := range squads {
			if !squadPattern.MatchString(id) {
				return nil, errors.New("Remnawave profile has an invalid squad UUID")
			}
		}
		ids := slices.Clone(squads)
		for i := range ids {
			ids[i] = strings.ToLower(ids[i])
		}
		slices.Sort(ids)
		profiles[name] = slices.Compact(ids)
	}
	return &Remnawave{
		hwidExternalSquadUUID: cfg.HWIDExternalSquadUUID,
		baseURL:               u, token: cfg.APIToken, profiles: profiles, caddyAPIKey: cfg.CaddyAPIKey,
		forwardedFor: cfg.ForwardedFor, forwardedProto: cfg.ForwardedProto,
		client: &http.Client{Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

type remnaUser struct {
	ExternalSquadUUID    *string    `json:"externalSquadUuid"`
	HWIDDeviceLimit      *int       `json:"hwidDeviceLimit"`
	ID                   int64      `json:"id"`
	Username             string     `json:"username"`
	ShortUUID            string     `json:"shortUuid"`
	Description          string     `json:"description"`
	Status               string     `json:"status"`
	TrafficLimitBytes    int64      `json:"trafficLimitBytes"`
	TrafficLimitStrategy string     `json:"trafficLimitStrategy"`
	ExpireAt             time.Time  `json:"expireAt"`
	LastTrafficResetAt   *time.Time `json:"lastTrafficResetAt"`
	ActiveInternalSquads []struct {
		UUID string `json:"uuid"`
	} `json:"activeInternalSquads"`
	UserTraffic *struct {
		UsedTrafficBytes         int64  `json:"usedTrafficBytes"`
		LifetimeUsedTrafficBytes *int64 `json:"lifetimeUsedTrafficBytes"`
	} `json:"userTraffic"`
}

func (u remnaUser) valid() bool {
	return u.ID > 0 && u.ID <= maxJSONInteger && usernamePattern.MatchString(u.Username) && u.UserTraffic != nil && u.UserTraffic.UsedTrafficBytes >= 0 && u.TrafficLimitBytes >= 0 && !u.ExpireAt.IsZero() && slices.Contains([]string{"ACTIVE", "DISABLED", "LIMITED", "EXPIRED"}, u.Status)
}

func (u remnaUser) remote() bundle.Remote {
	return effectiveRemote(bundle.Remote{ID: strconv.FormatInt(u.ID, 10), Username: u.Username, Status: u.Status, UsedBytes: u.UserTraffic.UsedTrafficBytes, LimitBytes: u.TrafficLimitBytes, ExpiresAt: u.ExpireAt, LastTrafficResetAt: u.LastTrafficResetAt, LifetimeUsedBytes: u.UserTraffic.LifetimeUsedTrafficBytes})
}

func effectiveRemote(r bundle.Remote) bundle.Remote {
	if r.Status == "ACTIVE" {
		if !r.ExpiresAt.After(time.Now()) {
			r.Status = "EXPIRED"
		} else if r.LimitBytes > 0 && r.UsedBytes >= r.LimitBytes {
			r.Status = "LIMITED"
		}
	}
	return r
}

func ownership(owner, key string) string { return "remna-quota:" + owner + ":" + key }

func (p *Remnawave) Validate(part bundle.PartRequest) error {
	if _, ok := p.profiles[part.Profile]; !ok || part.LimitBytes < 0 || part.LimitBytes > maxJSONInteger {
		return bundle.ErrInvalid
	}
	return nil
}

func (p *Remnawave) Ensure(ctx context.Context, owner string, part bundle.Part) (bundle.Remote, error) {
	return p.EnsureObserved(ctx, owner, part, nil)
}

func (p *Remnawave) EnsureObserved(ctx context.Context, owner string, part bundle.Part, save func(bundle.Remote) error) (bundle.Remote, error) {
	squads, ok := p.profiles[part.Profile]
	if !ok || owner == "" || part.Key == "" || !usernamePattern.MatchString(part.Username) || part.LimitBytes < 0 || part.LimitBytes > maxJSONInteger || part.ExpiresAt.IsZero() || !slices.Contains([]string{"NO_RESET", "DAY", "WEEK", "MONTH", "MONTH_ROLLING"}, part.ResetStrategy) {
		return bundle.Remote{}, bundle.ErrInvalid
	}
	marker := ownership(owner, part.Key)
	var user remnaUser
	var err error
	if part.Remote != nil && part.Remote.ID != "" {
		if !validID(part.Remote.ID) {
			return bundle.Remote{}, bundle.ErrInvalid
		}
		user, err = p.getUser(ctx, "/api/users/"+part.Remote.ID)
		if err != nil && !errors.Is(err, bundle.ErrNotFound) {
			return bundle.Remote{}, err
		}
		if err == nil && strconv.FormatInt(user.ID, 10) != part.Remote.ID {
			return bundle.Remote{}, bundle.ErrConflict
		}
	}
	if user.ID == 0 {
		user, err = p.getUser(ctx, "/api/users/by-username/"+url.PathEscape(part.Username))
		if err != nil && !errors.Is(err, bundle.ErrNotFound) {
			return bundle.Remote{}, err
		}
		if errors.Is(err, bundle.ErrNotFound) {
			creation, projectionErr := bundle.EnforcementPart(part, bundle.Remote{})
			if projectionErr != nil {
				return bundle.Remote{}, projectionErr
			}
			status := "ACTIVE"
			if !creation.Enabled || !part.ExpiresAt.After(time.Now()) {
				status = "DISABLED"
			}
			body := map[string]any{"username": part.Username, "description": marker, "status": status, "trafficLimitBytes": creation.LimitBytes, "trafficLimitStrategy": part.ResetStrategy, "expireAt": part.ExpiresAt.UTC(), "activeInternalSquads": squads}
			user, err = p.mutate(ctx, http.MethodPost, "/api/users", body, http.StatusCreated)
			if err != nil {
				// A timeout may occur after creation committed. Recover using the
				// exact ownership marker, never by username alone.
				recovered, recoveryErr := p.getUser(ctx, "/api/users/by-username/"+url.PathEscape(part.Username))
				if recoveryErr != nil {
					return bundle.Remote{}, err
				}
				user = recovered
			}
		}
	}
	if user.Description != marker || user.Username != part.Username {
		return bundle.Remote{}, bundle.ErrConflict
	}
	observeUser := func() error {
		if user.Description != marker || user.Username != part.Username {
			return bundle.ErrConflict
		}
		if save != nil {
			return save(user.remote())
		}
		return nil
	}
	if err := observeUser(); err != nil {
		return bundle.Remote{}, err
	}
	part, err = bundle.EnforcementPart(part, user.remote())
	if err != nil {
		return bundle.Remote{}, err
	}

	patch := map[string]any{"id": user.ID}
	if user.TrafficLimitBytes != part.LimitBytes {
		patch["trafficLimitBytes"] = part.LimitBytes
	}
	if user.TrafficLimitStrategy != part.ResetStrategy {
		patch["trafficLimitStrategy"] = part.ResetStrategy
	}
	// v3 rejects PATCH dates in the past. Disabled status enforces a desired
	// expired date even if an older panel expiry must remain on the record.
	if part.ExpiresAt.After(time.Now()) && !user.ExpireAt.Truncate(time.Millisecond).Equal(part.ExpiresAt.Truncate(time.Millisecond)) {
		patch["expireAt"] = part.ExpiresAt.UTC()
	}
	currentSquads := make([]string, 0, len(user.ActiveInternalSquads))
	for _, s := range user.ActiveInternalSquads {
		currentSquads = append(currentSquads, s.UUID)
	}
	slices.Sort(currentSquads)
	if !slices.Equal(currentSquads, squads) {
		patch["activeInternalSquads"] = squads
	}
	eligible := part.Enabled && part.ExpiresAt.After(time.Now()) && (part.LimitBytes == 0 || user.UserTraffic.UsedTrafficBytes < part.LimitBytes)
	// PATCH may reactivate LIMITED/EXPIRED users on quota increases/renewal.
	// Disable first when such an update must not grant access.
	mustDisable := !part.Enabled || !part.ExpiresAt.After(time.Now()) || (!eligible && (user.Status == "ACTIVE" || len(patch) > 1))
	if mustDisable && user.Status != "DISABLED" {
		user, err = p.mutate(ctx, http.MethodPost, "/api/users/"+strconv.FormatInt(user.ID, 10)+"/actions/disable", nil, http.StatusOK)
		if err != nil {
			return bundle.Remote{}, err
		}
		if err := observeUser(); err != nil {
			return bundle.Remote{}, err
		}
	}
	if len(patch) > 1 {
		user, err = p.mutate(ctx, http.MethodPatch, "/api/users", patch, http.StatusOK)
		if err != nil {
			return bundle.Remote{}, err
		}
		if err := observeUser(); err != nil {
			return bundle.Remote{}, err
		}
	}
	// Explicitly require eligibility under both desired and observed settings.
	// This restores access after renewal, quota removal/increase or a scheduled
	// reset, including when the panel still reports LIMITED or EXPIRED.
	eligible = part.Enabled && part.ExpiresAt.After(time.Now()) && user.ExpireAt.After(time.Now()) && (part.LimitBytes == 0 || user.UserTraffic.UsedTrafficBytes < part.LimitBytes) && (user.TrafficLimitBytes == 0 || user.UserTraffic.UsedTrafficBytes < user.TrafficLimitBytes)
	if eligible && user.Status != "ACTIVE" {
		user, err = p.mutate(ctx, http.MethodPost, "/api/users/"+strconv.FormatInt(user.ID, 10)+"/actions/enable", nil, http.StatusOK)
		if err != nil {
			return bundle.Remote{}, err
		}
		if err := observeUser(); err != nil {
			return bundle.Remote{}, err
		}
	}
	// Read the committed state instead of presenting desired settings as if
	// they had already been applied by the panel.
	user, err = p.getUser(ctx, "/api/users/"+strconv.FormatInt(user.ID, 10))
	if err != nil {
		return bundle.Remote{}, err
	}
	if user.Description != marker || user.Username != part.Username {
		return bundle.Remote{}, bundle.ErrConflict
	}
	if err := observeUser(); err != nil {
		return bundle.Remote{}, err
	}
	if !user.matches(part, squads) {
		return bundle.Remote{}, bundle.ErrUnavailable
	}
	return user.remote(), nil
}

func (u remnaUser) matches(part bundle.Part, squads []string) bool {
	if u.TrafficLimitBytes != part.LimitBytes || u.TrafficLimitStrategy != part.ResetStrategy {
		return false
	}
	if part.ExpiresAt.After(time.Now()) && !u.ExpireAt.Truncate(time.Millisecond).Equal(part.ExpiresAt.Truncate(time.Millisecond)) {
		return false
	}
	actual := make([]string, 0, len(u.ActiveInternalSquads))
	for _, s := range u.ActiveInternalSquads {
		actual = append(actual, strings.ToLower(s.UUID))
	}
	slices.Sort(actual)
	if !slices.Equal(actual, squads) {
		return false
	}
	if !part.Enabled || !part.ExpiresAt.After(time.Now()) {
		return u.Status == "DISABLED"
	}
	if part.LimitBytes > 0 && u.UserTraffic.UsedTrafficBytes >= part.LimitBytes {
		return u.Status != "ACTIVE"
	}
	return u.Status == "ACTIVE"
}

func validID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && n <= maxJSONInteger && strconv.FormatInt(n, 10) == id
}

// Delete permanently removes an owned user. A missing user is treated as success
// so the grace-period sweep is idempotent.
func (p *Remnawave) Delete(ctx context.Context, id string) error {
	if !validID(id) {
		return bundle.ErrInvalid
	}
	err := p.do(ctx, http.MethodDelete, "/api/users/"+id, nil, http.StatusNoContent, nil)
	if errors.Is(err, bundle.ErrNotFound) {
		return nil
	}
	return err
}

var shortUUIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ConfigJSON returns the panel-generated Xray JSON configs for one remote user
// (an array of full configurations, one per host).
func (p *Remnawave) ConfigJSON(ctx context.Context, id string) ([]byte, error) {
	if !validID(id) {
		return nil, bundle.ErrInvalid
	}
	user, err := p.getUser(ctx, "/api/users/"+id)
	if err != nil {
		return nil, err
	}
	if !shortUUIDPattern.MatchString(user.ShortUUID) {
		return nil, bundle.ErrConflict
	}
	var raw json.RawMessage
	if err := p.do(ctx, http.MethodGet, "/api/sub/"+user.ShortUUID+"/json", nil, http.StatusOK, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, bundle.ErrUnavailable
	}
	return raw, nil
}

func (p *Remnawave) Read(ctx context.Context, id string) (bundle.Remote, error) {
	if !validID(id) {
		return bundle.Remote{}, bundle.ErrInvalid
	}
	u, err := p.getUser(ctx, "/api/users/"+id)
	if err != nil {
		return bundle.Remote{}, err
	}
	if strconv.FormatInt(u.ID, 10) != id || !strings.HasPrefix(u.Description, "remna-quota:") {
		return bundle.Remote{}, bundle.ErrConflict
	}
	return u.remote(), nil
}

func (p *Remnawave) Links(ctx context.Context, id string) ([]string, error) {
	u, err := p.Read(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.Status != "ACTIVE" {
		return []string{}, nil
	}
	var envelope struct {
		Response struct {
			EnabledKeys []string `json:"enabledKeys"`
		} `json:"response"`
	}
	if err := p.do(ctx, http.MethodGet, "/api/subscriptions/connection-keys/"+id, nil, http.StatusOK, &envelope); err != nil {
		return nil, err
	}
	if envelope.Response.EnabledKeys == nil {
		return nil, bundle.ErrUnavailable
	}
	for _, link := range envelope.Response.EnabledKeys {
		if strings.ContainsAny(link, "\r\n") || !strings.Contains(link, "://") {
			return nil, bundle.ErrUnavailable
		}
	}
	return envelope.Response.EnabledKeys, nil
}

func (p *Remnawave) getUser(ctx context.Context, path string) (remnaUser, error) {
	return p.mutate(ctx, http.MethodGet, path, nil, http.StatusOK)
}

func (p *Remnawave) mutate(ctx context.Context, method, path string, body any, status int) (remnaUser, error) {
	var envelope struct {
		Response remnaUser `json:"response"`
	}
	if err := p.do(ctx, method, path, body, status, &envelope); err != nil {
		return remnaUser{}, err
	}
	if !envelope.Response.valid() {
		return remnaUser{}, bundle.ErrUnavailable
	}
	return envelope.Response, nil
}

func (p *Remnawave) do(ctx context.Context, method, path string, body any, status int, dst any) error {
	var payload io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return bundle.ErrInvalid
		}
		payload = bytes.NewReader(data)
	}
	u := *p.baseURL
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	req, err := http.NewRequestWithContext(ctx, method, u.String(), payload)
	if err != nil {
		return bundle.ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.caddyAPIKey != "" {
		req.Header.Set("X-Api-Key", p.caddyAPIKey)
	}
	if p.forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", p.forwardedFor)
	}
	if p.forwardedProto != "" {
		req.Header.Set("X-Forwarded-Proto", p.forwardedProto)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return bundle.ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			delay := time.Second
			if raw := resp.Header.Get("Retry-After"); raw != "" {
				if seconds, err := strconv.ParseInt(raw, 10, 32); err == nil && seconds >= 0 {
					delay = time.Duration(seconds) * time.Second
				} else if at, err := http.ParseTime(raw); err == nil {
					delay = max(time.Duration(0), time.Until(at))
				}
			}
			return &bundle.RetryError{At: time.Now().Add(delay), RateLimited: resp.StatusCode == http.StatusTooManyRequests}
		case http.StatusNotFound:
			return bundle.ErrNotFound
		case http.StatusConflict:
			return bundle.ErrConflict
		case http.StatusUnauthorized, http.StatusForbidden:
			return &AccessError{StatusCode: resp.StatusCode}
		default:
			return bundle.ErrUnavailable
		}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return bundle.ErrUnavailable
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return bundle.ErrUnavailable
	}
	return nil
}

var _ bundle.Provider = (*Remnawave)(nil)

func (p *Remnawave) Profiles() []string {
	out := make([]string, 0, len(p.profiles))
	for name := range p.profiles {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// String deliberately excludes credentials and upstream URLs from logs.
func (p *Remnawave) String() string { return fmt.Sprintf("Remnawave{profiles:%d}", len(p.profiles)) }
