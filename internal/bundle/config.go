package bundle

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ConfigForJSON builds the importable Xray JSON subscription for an account.
// Like the panel, the result is an ARRAY of complete configurations, one per
// host, so clients list every node (regular VPN and CDN) instead of showing only
// the first outbound of a single merged config. There is no balancer. Traffic
// accounting is unaffected because each outbound keeps its own credentials.
func (s *Service) ConfigForJSON(ctx context.Context, snapshot Bundle) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	release, err := s.acquireRead(ctx, snapshot.ID)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer release()

	b, err := s.repo.Get(ctx, snapshot.ID)
	if err != nil {
		return nil, err
	}
	if !b.Enabled || b.TokenHash != snapshot.TokenHash {
		return nil, ErrNotFound
	}
	return s.buildConfig(ctx, b, Device{})
}

// ConfigForJSONForDevice enforces the shared device limit before building the
// JSON config: when the account has a device limit, the caller must present a
// valid HWID and a slot is reserved for it (exactly like the link list).
func (s *Service) ConfigForJSONForDevice(ctx context.Context, snapshot Bundle, device Device) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	release, err := s.acquireRead(ctx, snapshot.ID)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer release()

	b, err := s.repo.Get(ctx, snapshot.ID)
	if err != nil {
		return nil, err
	}
	if !b.Enabled || b.TokenHash != snapshot.TokenHash {
		return nil, ErrNotFound
	}
	if DeviceLimit(b) > 0 {
		if !hwidPattern.MatchString(device.HWID) {
			return nil, ErrHWIDRequired
		}
		for _, part := range b.Parts {
			if part.AppliedRevision != b.Revision {
				return nil, ErrUnavailable
			}
		}
		b, err = s.reserveDevice(ctx, b, device)
		if err != nil {
			return nil, err
		}
		for _, part := range b.Parts {
			if err := s.syncDevices(ctx, b, part); err != nil {
				_ = s.recordFailure(ctx, b.ID, part.Key, b.Revision, err)
				return nil, ErrUnavailable
			}
		}
	}
	return s.buildConfig(ctx, b, device)
}

func (s *Service) buildConfig(ctx context.Context, b Bundle, device Device) ([]byte, error) {
	now := s.now().UTC()
	configs := []map[string]any{}
	for _, part := range b.Parts {
		if !part.Enabled || part.Remote == nil || part.Remote.ID == "" {
			continue
		}
		if !part.ExpiresAt.IsZero() && !part.ExpiresAt.After(now) {
			continue
		}
		provider := s.providers[part.Provider]
		configsProvider, ok := provider.(ConfigProvider)
		if !ok {
			continue
		}
		raw, err := configsProvider.ConfigJSON(ctx, part.Remote.ID, device)
		if err != nil {
			return nil, err
		}
		var parsed []map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			continue
		}
		for _, config := range parsed {
			if _, ok := config["outbounds"]; !ok {
				continue
			}
			if name, _ := config["remarks"].(string); strings.TrimSpace(name) == "" {
				config["remarks"] = part.Label
			}
			configs = append(configs, config)
		}
	}
	if len(configs) == 0 {
		return nil, ErrUnavailable
	}
	return json.Marshal(configs)
}
