package client

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

type Device struct {
	HWID       string    `json:"hwid"`
	Platform   string    `json:"platform,omitempty"`
	OSVersion  string    `json:"os_version,omitempty"`
	Model      string    `json:"model,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}
type DeviceList struct {
	DeviceLimit int      `json:"device_limit"`
	DeviceCount int      `json:"device_count"`
	Items       []Device `json:"items"`
}

func (c *Client) ListDevices(ctx context.Context, id string) (DeviceList, error) {
	var out DeviceList
	if !accountIDPattern.MatchString(id) {
		return out, ErrInvalidRequest
	}
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/accounts/"+url.PathEscape(id)+"/devices", nil, nil, &out)
	if err == nil && (out.Items == nil || out.DeviceCount != len(out.Items)) {
		return out, ErrInvalidResponse
	}
	return out, err
}

var hwidPattern = regexp.MustCompile(`^[a-zA-Z0-9=-]{10,64}$`)

func (c *Client) DeleteDevice(ctx context.Context, id, hwid string) (Account, error) {
	if !hwidPattern.MatchString(hwid) {
		return Account{}, ErrInvalidRequest
	}
	return c.request(ctx, http.MethodDelete, id, "devices/"+url.PathEscape(hwid), false, nil)
}
