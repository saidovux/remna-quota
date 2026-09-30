package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

const testSquad = "11111111-1111-4111-8111-111111111111"

func TestInvalidProfileRejectedBeforePersistence(t *testing.T) {
	p := panelProvider(t, &panelStub{})
	repo := bundle.NewMemoryRepository()
	service := bundle.NewService(repo, map[string]bundle.Provider{"remnawave": p})
	part := testPart().PartRequest
	part.Provider, part.Profile = "remnawave", "typo-profile"
	_, err := service.Put(context.Background(), "site-account", bundle.PutRequest{Username: "alice", Enabled: true, Parts: []bundle.PartRequest{part}})
	if !errors.Is(err, bundle.ErrInvalid) {
		t.Fatalf("invalid profile accepted: %v", err)
	}
	if _, err := repo.Get(context.Background(), "site-account"); !errors.Is(err, bundle.ErrNotFound) {
		t.Fatalf("invalid immutable profile persisted: %v", err)
	}
}

func testPart() bundle.Part {
	return bundle.Part{PartRequest: bundle.PartRequest{Key: "cdn", Profile: "cdn", LimitBytes: 100, ExpiresAt: time.Now().UTC().Add(24 * time.Hour).Truncate(time.Millisecond), Enabled: true, ResetStrategy: "MONTH"}, Username: "alice_cdn"}
}

type panelStub struct {
	user                                             map[string]any
	creates, patches, enables, disables, connections int
	ambiguousCreate                                  bool
	ignorePatch, skipAutoEnable                      bool
	t                                                *testing.T
}

func (s *panelStub) handler(w http.ResponseWriter, r *http.Request) {
	s.t.Helper()
	if r.Header.Get("Authorization") != "Bearer test-secret" {
		s.t.Error("missing API authentication")
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/api/users/by-username/alice_cdn" || r.URL.Path == "/api/users/42"):
		if s.user == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
	case r.Method == http.MethodPost && r.URL.Path == "/api/users":
		s.creates++
		if s.user != nil {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&s.user); err != nil {
			s.t.Fatal(err)
		}
		s.user["id"] = float64(42)
		s.user["userTraffic"] = map[string]any{"usedTrafficBytes": float64(0)}
		s.normalizeSquads()
		if s.ambiguousCreate {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPatch && r.URL.Path == "/api/users":
		s.patches++
		var patch map[string]any
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
			s.t.Fatal(err)
		}
		if _, ok := patch["status"]; ok {
			s.t.Error("adapter must not blindly patch status")
		}
		if s.ignorePatch {
			break
		}
		if limit, ok := patch["trafficLimitBytes"].(float64); ok && !s.skipAutoEnable && s.user["status"] == "LIMITED" && (limit > s.user["trafficLimitBytes"].(float64) || limit == 0) {
			s.user["status"] = "ACTIVE"
		}
		if _, ok := patch["expireAt"]; ok && s.user["status"] == "EXPIRED" {
			s.user["status"] = "ACTIVE"
		}
		for k, v := range patch {
			s.user[k] = v
		}
		s.normalizeSquads()
	case r.Method == http.MethodPost && r.URL.Path == "/api/users/42/actions/disable":
		s.disables++
		s.user["status"] = "DISABLED"
	case r.Method == http.MethodPost && r.URL.Path == "/api/users/42/actions/enable":
		s.enables++
		s.user["status"] = "ACTIVE"
	case r.Method == http.MethodGet && r.URL.Path == "/api/subscriptions/connection-keys/42":
		s.connections++
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"enabledKeys": []string{"vless://secret@enabled.invalid:443#main"}, "hiddenKeys": []string{"vless://secret@hidden.invalid:443#hidden"}, "disabledKeys": []string{"vless://secret@disabled.invalid:443#disabled"}}})
		return
	default:
		s.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"response": s.user})
}

func (s *panelStub) normalizeSquads() {
	items, ok := s.user["activeInternalSquads"].([]any)
	if !ok {
		return
	}
	for i, item := range items {
		if id, ok := item.(string); ok {
			items[i] = map[string]any{"uuid": id, "name": "CDN"}
		}
	}
}

func panelProvider(t *testing.T, s *panelStub) *Remnawave {
	t.Helper()
	s.t = t
	server := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(server.Close)
	p, err := NewRemnawave(RemnaConfig{BaseURL: server.URL, APIToken: "test-secret", Profiles: map[string][]string{"cdn": {testSquad}}, AllowHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnsureCreateIndependentNativeQuotaAndIdempotency(t *testing.T) {
	s := &panelStub{}
	p := panelProvider(t, s)
	part := testPart()
	r, err := p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "42" || r.Username != "alice_cdn" || r.LimitBytes != 100 || s.user["description"] != "remna-quota:owner:cdn" {
		t.Fatalf("bad provisioning: %#v", r)
	}
	part.Remote = &r
	if _, err := p.Ensure(context.Background(), "owner", part); err != nil {
		t.Fatal(err)
	}
	if s.creates != 1 || s.patches != 0 || s.enables != 0 || s.disables != 0 {
		t.Fatalf("non-idempotent requests: %+v", s)
	}
}

func TestEnsureRecoversCommittedCreateAfterUpstreamFailure(t *testing.T) {
	s := &panelStub{ambiguousCreate: true}
	p := panelProvider(t, s)
	if _, err := p.Ensure(context.Background(), "owner", testPart()); err != nil {
		t.Fatal(err)
	}
	if s.creates != 1 {
		t.Fatalf("creates=%d", s.creates)
	}
}

func TestEnsureRefusesForeignUsernameAndSavedID(t *testing.T) {
	s := &panelStub{}
	p := panelProvider(t, s)
	part := testPart()
	r, err := p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	s.user["description"] = "unrelated customer"
	for _, remote := range []*bundle.Remote{nil, &r} {
		part.Remote = remote
		if _, err := p.Ensure(context.Background(), "owner", part); !errors.Is(err, bundle.ErrConflict) {
			t.Fatalf("expected conflict, got %v", err)
		}
	}
	if s.patches != 0 || s.enables != 0 || s.disables != 0 {
		t.Fatal("foreign account mutated")
	}
}

func TestEnsurePreservesExhaustionAndCounters(t *testing.T) {
	s := &panelStub{}
	p := panelProvider(t, s)
	part := testPart()
	r, err := p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	part.Remote = &r
	s.user["status"] = "LIMITED"
	s.user["userTraffic"].(map[string]any)["usedTrafficBytes"] = float64(150)
	r, err = p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "LIMITED" || r.UsedBytes != 150 || s.patches != 0 || s.enables != 0 || s.disables != 0 {
		t.Fatalf("exhausted user changed: %#v", r)
	}
	// Remnawave itself reactivates LIMITED on any increase. An increase that
	// remains below observed usage must not unlock traffic even transiently.
	part.LimitBytes = 120
	r, err = p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "DISABLED" || r.UsedBytes != 150 || s.disables != 1 || s.enables != 0 {
		t.Fatalf("unsafe quota increase: %#v", r)
	}
	part.LimitBytes = 200
	r, err = p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ACTIVE" || r.UsedBytes != 150 || s.enables != 1 {
		t.Fatalf("eligible increase failed: %#v", r)
	}
}

func TestDisableLimitedUserAndReenableOnlyAfterEligibility(t *testing.T) {
	s := &panelStub{}
	p := panelProvider(t, s)
	part := testPart()
	r, err := p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	part.Remote = &r
	s.user["status"] = "LIMITED"
	s.user["userTraffic"].(map[string]any)["usedTrafficBytes"] = float64(150)
	part.Enabled = false
	if _, err := p.Ensure(context.Background(), "owner", part); err != nil {
		t.Fatal(err)
	}
	part.Enabled = true
	r, err = p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "DISABLED" || s.enables != 0 {
		t.Fatal("reenabled exhausted account")
	}
	// Simulate the panel's scheduled reset; reconciliation may now enable it.
	s.user["userTraffic"].(map[string]any)["usedTrafficBytes"] = float64(0)
	s.user["lastTrafficResetAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	r, err = p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ACTIVE" || s.enables != 1 {
		t.Fatal("scheduled reset did not recover account")
	}
}

func TestLinksOnlyEnabledHostsAndEligibleUser(t *testing.T) {
	s := &panelStub{}
	p := panelProvider(t, s)
	if _, err := p.Ensure(context.Background(), "owner", testPart()); err != nil {
		t.Fatal(err)
	}
	links, err := p.Links(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || !strings.Contains(links[0], "enabled.invalid") {
		t.Fatalf("links=%v", links)
	}
	// The scheduler may not yet have flipped status to LIMITED.
	s.user["userTraffic"].(map[string]any)["usedTrafficBytes"] = float64(100)
	links, err = p.Links(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 || s.connections != 1 {
		t.Fatal("exhausted user received connection keys")
	}
}

func TestRemnawaveRejectsRedirectAndScrubsErrors(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/secret")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte("upstream-secret"))
	}))
	defer origin.Close()
	p, err := NewRemnawave(RemnaConfig{BaseURL: origin.URL, APIToken: "test-secret", AllowHTTP: true, Profiles: map[string][]string{"cdn": {testSquad}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Read(context.Background(), "42")
	if !errors.Is(err, bundle.ErrUnavailable) || reached || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect or error leak: %v", err)
	}
}

func TestRemnawaveRejectsUnsupportedResponseContract(t *testing.T) {
	s := &panelStub{}
	p := panelProvider(t, s)
	if _, err := p.Ensure(context.Background(), "owner", testPart()); err != nil {
		t.Fatal(err)
	}
	delete(s.user, "userTraffic")
	if _, err := p.Read(context.Background(), "42"); !errors.Is(err, bundle.ErrUnavailable) {
		t.Fatalf("unsupported v2 contract accepted: %v", err)
	}
}

func TestRemnawaveConfigurationValidation(t *testing.T) {
	for _, base := range []string{"http://panel.invalid", "https://user:password@panel.invalid", "https://panel.invalid?token=secret", "https://panel.invalid#secret", "file:///etc/passwd"} {
		if _, err := NewRemnawave(RemnaConfig{BaseURL: base, APIToken: "secret", Profiles: map[string][]string{"cdn": {testSquad}}}); err == nil {
			t.Fatalf("accepted URL %q", base)
		}
	}
}

func TestRemovingQuotaExplicitlyRecoversEligibleLimitedUser(t *testing.T) {
	s := &panelStub{skipAutoEnable: true}
	p := panelProvider(t, s)
	part := testPart()
	r, err := p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	part.Remote = &r
	s.user["status"] = "LIMITED"
	s.user["userTraffic"].(map[string]any)["usedTrafficBytes"] = float64(150)
	part.LimitBytes = 0
	r, err = p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "ACTIVE" || r.LimitBytes != 0 || r.UsedBytes != 150 || s.enables != 1 {
		t.Fatalf("quota removal did not restore access without resetting usage: %#v", r)
	}
}

func TestLoweringQuotaBelowUsageDisablesBeforePatch(t *testing.T) {
	s := &panelStub{}
	p := panelProvider(t, s)
	part := testPart()
	r, err := p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	part.Remote = &r
	s.user["userTraffic"].(map[string]any)["usedTrafficBytes"] = float64(50)
	part.LimitBytes = 25
	r, err = p.Ensure(context.Background(), "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "DISABLED" || r.UsedBytes != 50 || s.enables != 0 || s.disables != 1 {
		t.Fatalf("lower quota failed to preserve exhaustion: %#v", r)
	}
}

func TestEnsureRejectsIgnoredPanelMutations(t *testing.T) {
	for _, field := range []string{"limit", "strategy", "expiry", "squads"} {
		t.Run(field, func(t *testing.T) {
			s := &panelStub{ignorePatch: true}
			p := panelProvider(t, s)
			part := testPart()
			r, err := p.Ensure(context.Background(), "owner", part)
			if err != nil {
				t.Fatal(err)
			}
			part.Remote = &r
			switch field {
			case "limit":
				part.LimitBytes = 200
			case "strategy":
				part.ResetStrategy = "DAY"
			case "expiry":
				part.ExpiresAt = part.ExpiresAt.Add(24 * time.Hour)
			case "squads":
				p.profiles["cdn"] = []string{"22222222-2222-4222-8222-222222222222"}
			}
			if _, err := p.Ensure(context.Background(), "owner", part); !errors.Is(err, bundle.ErrUnavailable) {
				t.Fatalf("ignored mutation reported success: %v", err)
			}
		})
	}
}

func TestConfigJSONForwardsDeviceHWID(t *testing.T) {
	var gotPath, gotHWID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/users/42":
			_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{
				"id":                42,
				"username":          "alice_cdn",
				"status":            "ACTIVE",
				"shortUuid":         "short_1",
				"description":       "remna-quota:owner:cdn",
				"expireAt":          "2030-01-01T00:00:00.000Z",
				"trafficLimitBytes": 0,
				"userTraffic":       map[string]any{"usedTrafficBytes": 0, "lifetimeUsedTrafficBytes": 0},
			}})
		case "/api/sub/short_1/json":
			gotPath = r.URL.Path
			gotHWID = r.Header.Get("x-hwid")
			_, _ = w.Write([]byte(`[{"remarks":"real host","outbounds":[{"tag":"proxy","protocol":"vless"}]}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	p, err := NewRemnawave(RemnaConfig{BaseURL: server.URL, APIToken: "test-secret", AllowHTTP: true, Profiles: map[string][]string{"cdn": {testSquad}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.ConfigJSON(context.Background(), "42", bundle.Device{HWID: "DEVICE00001", Platform: "Android", OSVersion: "15", Model: "Pixel"})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/sub/short_1/json" {
		t.Fatalf("requested %q", gotPath)
	}
	if gotHWID != "DEVICE00001" {
		t.Fatalf("x-hwid = %q, want DEVICE00001 (config would be a placeholder)", gotHWID)
	}
	if !strings.Contains(string(raw), "real host") {
		t.Fatalf("config not returned: %s", raw)
	}
}
