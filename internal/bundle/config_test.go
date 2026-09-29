package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestConfigForJSONMergesPartOutbounds(t *testing.T) {
	s, _, req := setupService(t)
	b := mustPut(t, s, req)

	raw, err := s.ConfigForJSON(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("result is not a single config: %v", err)
	}
	outbounds, _ := cfg["outbounds"].([]any)
	if len(outbounds) != 2 {
		t.Fatalf("outbounds=%d, want 2 (one per part)", len(outbounds))
	}
	first, _ := outbounds[0].(map[string]any)
	second, _ := outbounds[1].(map[string]any)
	if first["tag"] != "proxy" || second["tag"] != "proxy-2" {
		t.Fatalf("duplicate tags not renamed: %v / %v", first["tag"], second["tag"])
	}
}

func TestConfigForJSONForDeviceReservesSlot(t *testing.T) {
	s, _, req := setupService(t)
	limit := 1
	req.DeviceLimit = &limit
	b := mustPut(t, s, req)

	if _, err := s.ConfigForJSONForDevice(context.Background(), b, Device{HWID: "DEVICE00001"}); err != nil {
		t.Fatalf("first device rejected: %v", err)
	}
	// The same device reuses its slot.
	if _, err := s.ConfigForJSONForDevice(context.Background(), b, Device{HWID: "DEVICE00001"}); err != nil {
		t.Fatalf("same device rejected: %v", err)
	}
	// A second distinct device must hit the shared limit.
	if _, err := s.ConfigForJSONForDevice(context.Background(), b, Device{HWID: "DEVICE00002"}); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("second device: got %v, want device limit", err)
	}
	// A missing HWID is rejected when a limit is configured.
	if _, err := s.ConfigForJSONForDevice(context.Background(), b, Device{}); !errors.Is(err, ErrHWIDRequired) {
		t.Fatalf("missing hwid: got %v, want required", err)
	}
}
