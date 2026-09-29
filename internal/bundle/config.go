package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ConfigForJSON builds one importable Xray JSON configuration from every active
// part of the account: the first panel config is the skeleton (dns/routing/
// inbounds) and all outbounds from every part are merged into it. There is no
// balancer — the first outbound remains the default route. Traffic accounting is
// unaffected because each outbound keeps its own user credentials.
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
	return s.buildConfig(ctx, b)
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
	return s.buildConfig(ctx, b)
}

func (s *Service) buildConfig(ctx context.Context, b Bundle) ([]byte, error) {
	now := s.now().UTC()
	var skeleton map[string]any
	seen := map[string]bool{}
	outbounds := []any{}
	for _, part := range b.Parts {
		if !part.Enabled || part.Remote == nil || part.Remote.ID == "" {
			continue
		}
		if !part.ExpiresAt.IsZero() && !part.ExpiresAt.After(now) {
			continue
		}
		provider := s.providers[part.Provider]
		configs, ok := provider.(ConfigProvider)
		if !ok {
			continue
		}
		raw, err := configs.ConfigJSON(ctx, part.Remote.ID)
		if err != nil {
			return nil, err
		}
		var parsed []map[string]any
		if err := json.Unmarshal(raw, &parsed); err != nil {
			continue
		}
		for _, cfg := range parsed {
			list, _ := cfg["outbounds"].([]any)
			for _, entry := range list {
				if object, ok := entry.(map[string]any); ok {
					tag, _ := object["tag"].(string)
					if tag == "" {
						tag = "proxy"
					}
					if seen[tag] {
						n := 2
						for seen[fmt.Sprintf("%s-%d", tag, n)] {
							n++
						}
						object["tag"] = fmt.Sprintf("%s-%d", tag, n)
					}
					seen[object["tag"].(string)] = true
				}
				outbounds = append(outbounds, entry)
			}
			if skeleton == nil {
				skeleton = cfg
			}
		}
	}
	if skeleton == nil || len(outbounds) == 0 {
		return nil, ErrUnavailable
	}
	skeleton["outbounds"] = outbounds
	return json.Marshal(skeleton)
}
