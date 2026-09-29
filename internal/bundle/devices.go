package bundle

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Device belongs to an account, never to an individual traffic part.
type Device struct {
	HWID       string    `json:"hwid"`
	Platform   string    `json:"platform,omitempty"`
	OSVersion  string    `json:"os_version,omitempty"`
	Model      string    `json:"model,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

// DeviceProvider mirrors the account's authoritative device policy using the
// provider's standard API. The caller holds the account operation lock.
type DeviceProvider interface {
	SyncDevices(context.Context, string, Part, int, []Device) error
}

var hwidPattern = regexp.MustCompile(`^[a-zA-Z0-9=-]{10,64}$`)

func DeviceLimit(b Bundle) int {
	if b.DeviceLimit == nil {
		return 0
	}
	return *b.DeviceLimit
}

func (s *Service) validateDeviceLimit(limit *int, parts []Part) error {
	if limit == nil {
		return nil
	}
	if *limit < 0 || *limit > 1000 {
		return fmt.Errorf("%w: device_limit must be 0..1000", ErrInvalid)
	}
	for _, p := range parts {
		if _, ok := s.providers[p.Provider].(DeviceProvider); !ok {
			return fmt.Errorf("%w: provider does not support devices", ErrInvalid)
		}
	}
	return nil
}

func (s *Service) syncDevices(ctx context.Context, b Bundle, p Part) error {
	if b.DeviceLimit == nil {
		return nil
	}
	provider, ok := s.providers[p.Provider].(DeviceProvider)
	if !ok {
		return ErrInvalid
	}
	started := time.Now()
	err := provider.SyncDevices(ctx, b.ID, p, *b.DeviceLimit, b.Devices)
	s.trackOperation("devices", started, err)
	return err
}

func deviceMetadata(value string) string {
	if !utf8.ValidString(value) || len(value) > 256 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return ""
	}
	return value
}

// reserveDevice is called under the operation lock. Durable registration precedes
// panel writes, so retries after disconnects/restarts cannot consume two slots.
func (s *Service) reserveDevice(ctx context.Context, snapshot Bundle, device Device) (Bundle, error) {
	if DeviceLimit(snapshot) == 0 {
		return snapshot, nil
	}
	if !hwidPattern.MatchString(device.HWID) {
		return Bundle{}, ErrHWIDRequired
	}
	return s.repo.Update(ctx, snapshot.ID, func(b *Bundle) error {
		if !b.Enabled || b.TokenHash != snapshot.TokenHash {
			return ErrNotFound
		}
		if b.Revision != snapshot.Revision {
			return ErrUnavailable
		}
		now := s.now().UTC()
		for i := range b.Devices {
			if b.Devices[i].HWID == device.HWID {
				b.Devices[i].LastSeenAt = now
				return nil
			}
		}
		if len(b.Devices) >= DeviceLimit(*b) {
			return ErrDeviceLimit
		}
		device.Platform = deviceMetadata(device.Platform)
		device.OSVersion = deviceMetadata(device.OSVersion)
		device.Model = deviceMetadata(device.Model)
		device.UserAgent = deviceMetadata(device.UserAgent)
		device.CreatedAt, device.LastSeenAt = now, now
		b.Devices = append(b.Devices, device)
		b.NextSyncAt = now
		return nil
	})
}

// DeleteDevice releases one shared slot. It does not revoke previously downloaded
// VPN credentials; HWID controls subscription retrieval, not established tunnels.
func (s *Service) DeleteDevice(ctx context.Context, id, hwid string) (Bundle, error) {
	if !accountIDPattern.MatchString(id) || !hwidPattern.MatchString(hwid) {
		return Bundle{}, ErrInvalid
	}
	// Intent updates use a short transaction and revision fence. Sync holds the
	// operation lock; a slow provider cannot delay accepting this deletion.
	_, err := s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		for i, d := range b.Devices {
			if d.HWID == hwid {
				b.Devices = append(b.Devices[:i], b.Devices[i+1:]...)
				b.Revision++
				markPending(b)
				b.UpdatedAt = s.now().UTC()
				b.NextSyncAt, b.NextRetryAt, b.FailureCount = s.now().UTC(), nil, 0
				return nil
			}
		}
		return nil
	})
	if err != nil {
		return Bundle{}, err
	}
	return s.Sync(ctx, id)
}
