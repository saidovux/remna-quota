package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/aggregate"
)

func TestExistingSubscriptionsAreNeverAdoptedOrMutated(t *testing.T) {
	var mutations atomic.Int32
	expires := time.Now().UTC().Add(time.Hour)
	panel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer read-only-test-key" {
			t.Error("missing API token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/users/51":
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"id": 51, "username": "created_by_another_app", "description": "Owned and renewed externally", "status": "ACTIVE", "trafficLimitBytes": 1000, "trafficLimitStrategy": "NO_RESET", "expireAt": expires, "userTraffic": map[string]any{"usedTrafficBytes": 123}}})
		case r.URL.Path == "/api/subscriptions/connection-keys/51":
			_, _ = w.Write([]byte(`{"response":{"enabledKeys":["vless://private-credential@example.invalid:443#original"]}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer panel.Close()
	p, err := NewRemnawaveReferences(RemnaConfig{BaseURL: panel.URL, APIToken: "read-only-test-key", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	service := aggregate.NewService(aggregate.NewMemoryRepository(), map[string]aggregate.Provider{"panel": p})
	b, err := service.Put(ctx, "portable", aggregate.PutRequest{Enabled: true, Sources: []aggregate.SourceInput{{Key: "main", Label: "MAIN", Provider: "panel", Reference: "51", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	links, err := service.Links(ctx, b)
	if err != nil || len(links) != 1 || !strings.Contains(links[0], "MAIN") {
		t.Fatal("foreign-owned subscription could not be combined read-only")
	}
	b, err = service.Get(ctx, b.ID, true)
	if err != nil || b.Sources[0].Snapshot.UsedBytes != 123 {
		t.Fatal("external quota not read")
	}
	no := false
	if _, err := service.Patch(ctx, b.ID, aggregate.PatchRequest{Enabled: &no}); err != nil {
		t.Fatal(err)
	}
	if err := service.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	if mutations.Load() != 0 {
		t.Fatal("aggregation wrote to panel")
	}
	// The opt-in provisioning adapter must still refuse taking over this user.
	managed, err := NewRemnawave(RemnaConfig{BaseURL: panel.URL, APIToken: "read-only-test-key", AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := managed.Read(ctx, "51"); err == nil {
		t.Fatal("managed ownership protection was weakened")
	}
}
