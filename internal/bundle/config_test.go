package bundle

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestConfigForJSONListsEveryHost(t *testing.T) {
	s, _, req := setupService(t)
	b := mustPut(t, s, req)

	raw, err := s.ConfigForJSON(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	var configs []map[string]any
	if err := json.Unmarshal(raw, &configs); err != nil {
		t.Fatalf("result is not a config array: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("configs=%d, want 2 (one per part)", len(configs))
	}
	for _, config := range configs {
		outbounds, _ := config["outbounds"].([]any)
		if len(outbounds) != 1 {
			t.Fatalf("each host config should keep one proxy outbound, got %d", len(outbounds))
		}
		if name, _ := config["remarks"].(string); name == "" {
			t.Fatalf("config missing remarks: %v", config)
		}
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
