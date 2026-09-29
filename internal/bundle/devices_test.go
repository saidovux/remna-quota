package bundle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

type deviceFake struct {
	*fakeProvider
	deviceMu sync.Mutex
	mirrored map[string][]Device
	fail     bool
}

func (p *deviceFake) SyncDevices(ctx context.Context, owner string, part Part, limit int, devices []Device) error {
	p.deviceMu.Lock()
	defer p.deviceMu.Unlock()
	if p.fail {
		return ErrUnavailable
	}
	p.mirrored[part.Key] = append([]Device(nil), devices...)
	return nil
}
func deviceSetup(t *testing.T, limit int) (*Service, *deviceFake, Bundle, PutRequest) {
	s, base, req := setupService(t)
	p := &deviceFake{fakeProvider: base, mirrored: map[string][]Device{}}
	s.providers["vpn"] = p
	req.DeviceLimit = &limit
	b := mustPut(t, s, req)
	return s, p, b, req
}
func TestSharedDevicesConcurrentRestartAndRotation(t *testing.T) {
	s, p, b, _ := deviceSetup(t, 2)
	ctx := context.Background()
	other := NewService(s.repo, s.providers)
	other.now = s.now
	var wg sync.WaitGroup
	successes := 0
	var mu sync.Mutex
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			svc := s
			if i%2 == 1 {
				svc = other
			}
			links, err := svc.LinksForDevice(ctx, b, Device{HWID: fmt.Sprintf("device-%010d", i)})
			if err == nil {
				if len(links) != 2 {
					t.Errorf("links=%v", links)
				}
				mu.Lock()
				successes++
				mu.Unlock()
			} else if !errors.Is(err, ErrDeviceLimit) {
				t.Errorf("registration: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if successes != 2 {
		t.Fatalf("admitted %d", successes)
	}
	got, _ := s.Get(ctx, b.ID, false)
	if len(got.Devices) != 2 || len(p.mirrored["main"]) != 2 || len(p.mirrored["cdn"]) != 2 {
		t.Fatal("independent device pools")
	}
	for _, svc := range []*Service{s, other} {
		if _, err := svc.LinksForDevice(ctx, got, got.Devices[0]); err != nil {
			t.Fatal(err)
		}
	}
	// Exhausting CDN leaves MAIN and the same shared device slots available.
	p.mu.Lock()
	cdn := p.users[b.ID+"/cdn"]
	cdn.Status = "LIMITED"
	cdn.UsedBytes = cdn.LimitBytes
	p.users[b.ID+"/cdn"] = cdn
	p.mu.Unlock()
	if links, err := s.LinksForDevice(ctx, got, got.Devices[0]); err != nil || len(links) != 1 {
		t.Fatal("CDN exhaustion affected device access to MAIN", err)
	}
	rotated, err := s.RotateToken(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.LinksForDevice(ctx, rotated, Device{HWID: "device-9999999999"}); !errors.Is(err, ErrDeviceLimit) {
		t.Fatal("rotation replenished device slots", err)
	}
	if _, err = s.Links(ctx, rotated); !errors.Is(err, ErrHWIDRequired) {
		t.Fatal("non-HWID bypass", err)
	}
	if _, err = s.LinksForDevice(ctx, b, got.Devices[0]); !errors.Is(err, ErrNotFound) {
		t.Fatal("old token accepted", err)
	}
}

func TestDevicesFailureRecoveryLimitEditsAndDeletion(t *testing.T) {
	s, p, b, req := deviceSetup(t, 2)
	ctx := context.Background()
	p.fail = true
	device := Device{HWID: "device-1234567890"}
	if _, err := s.LinksForDevice(ctx, b, device); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, b.ID, false)
	if len(got.Devices) != 1 {
		t.Fatal("registration not durable")
	}
	p.fail = false
	got, err := s.Sync(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.LinksForDevice(ctx, got, device); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LinksForDevice(ctx, got, Device{HWID: "device-abcdefghij"}); err != nil {
		t.Fatal(err)
	}
	one := 1
	if _, err = s.Patch(ctx, b.ID, PatchRequest{DeviceLimit: &one}); !errors.Is(err, ErrConflict) {
		t.Fatal("lowered below registered devices", err)
	}
	// Old integrations omitting the field cannot inadvertently disable HWID.
	req.DeviceLimit = nil
	got, err = s.Put(ctx, b.ID, req)
	if err != nil || DeviceLimit(got) != 2 {
		t.Fatal("omission cleared policy", err)
	}
	got, err = s.DeleteDevice(ctx, b.ID, device.HWID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Devices) != 1 || len(p.mirrored["main"]) != 1 || len(p.mirrored["cdn"]) != 1 {
		t.Fatal("delete not shared")
	}
	if _, err = s.Patch(ctx, b.ID, PatchRequest{DeviceLimit: &one}); err != nil {
		t.Fatal(err)
	}
	zero := 0
	got, err = s.Patch(ctx, b.ID, PatchRequest{DeviceLimit: &zero})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Links(ctx, got); err != nil {
		t.Fatal("explicit disable failed", err)
	}
}
