package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saidovux/remna-quota/internal/bundle"
)

func TestRemnaDevicesConvergeAfterLostResponseAndProtectOwnership(t *testing.T) {
	stub := &panelStub{t: t}
	devices := map[string]bool{"foreign-device1": true}
	lost := true
	creates := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/hwid/devices/42" || r.URL.Path == "/api/hwid/devices" || r.URL.Path == "/api/hwid/devices/delete" {
			if r.Header.Get("Authorization") != "Bearer test-secret" {
				t.Error("missing token")
			}
			if r.Method == "POST" {
				var body struct {
					HWID   string `json:"hwid"`
					UserID int64  `json:"userId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.UserID != 42 {
					t.Error("wrong native user")
				}
				if r.URL.Path == "/api/hwid/devices/delete" {
					delete(devices, body.HWID)
				} else {
					devices[body.HWID] = true
					creates++
					if lost {
						lost = false
						w.WriteHeader(503)
						return
					}
				}
			}
			items := []map[string]any{}
			for hwid := range devices {
				items = append(items, map[string]any{"hwid": hwid, "userId": 42})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"total": len(items), "devices": items}})
			return
		}
		stub.handler(w, r)
	}))
	defer server.Close()
	p, err := NewRemnawave(RemnaConfig{BaseURL: server.URL, AllowHTTP: true, APIToken: "test-secret", Profiles: map[string][]string{"cdn": {testSquad}}, HWIDExternalSquadUUID: testSquad})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	part := testPart()
	remote, err := p.Ensure(ctx, "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	part.Remote = &remote
	want := []bundle.Device{{HWID: "shared-device1"}}
	if err = p.SyncDevices(ctx, "owner", part, 2, want); !errors.Is(err, bundle.ErrUnavailable) {
		t.Fatal(err)
	}
	if err = p.SyncDevices(ctx, "owner", part, 2, want); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || len(devices) != 1 || !devices[want[0].HWID] {
		t.Fatal("lost response duplicated slots")
	}
	if stub.user["hwidDeviceLimit"] != float64(2) || stub.user["externalSquadUuid"] != testSquad {
		t.Fatal("native policy not applied")
	}
	if err = p.SyncDevices(ctx, "intruder", part, 0, nil); !errors.Is(err, bundle.ErrConflict) {
		t.Fatal("ownership bypass", err)
	}
	if err = p.SyncDevices(ctx, "owner", part, 0, nil); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 || stub.user["hwidDeviceLimit"] != float64(0) {
		t.Fatal("disable failed")
	}
}
