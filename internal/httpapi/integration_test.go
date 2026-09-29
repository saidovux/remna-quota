package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

type integrationProvider struct{ users map[string]bundle.Remote }

func (p *integrationProvider) Ensure(_ context.Context, owner string, part bundle.Part) (bundle.Remote, error) {
	id := owner + "_" + part.Key
	r := p.users[id]
	r.ID, r.Username, r.LimitBytes, r.ExpiresAt = id, part.Username, part.LimitBytes, part.ExpiresAt
	r.Status = "ACTIVE"
	if !part.Enabled {
		r.Status = "DISABLED"
	}
	p.users[id] = r
	return r, nil
}
func (p *integrationProvider) Read(_ context.Context, id string) (bundle.Remote, error) {
	return p.users[id], nil
}
func (p *integrationProvider) Links(_ context.Context, id string) ([]string, error) {
	return []string{"vless://" + id + "@vpn.example:443#server"}, nil
}

// This exercises the actual service, repository, merger, and HTTP DTO together.
func TestAccountLifecycleWithIndependentEnforcement(t *testing.T) {
	provider := &integrationProvider{users: map[string]bundle.Remote{}}
	repo := bundle.NewMemoryRepository()
	service := bundle.NewService(repo, map[string]bundle.Provider{"test": provider})
	h, err := New(service, repo, Config{APIKey: testAPIKey, PublicURL: "https://subscriptions.example"})
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(24 * time.Hour)
	request := bundle.PutRequest{Username: "alice", Enabled: true, Parts: []bundle.PartRequest{
		{Key: "main", Label: "MAIN", Provider: "test", Profile: "main", Enabled: true, ExpiresAt: expires},
		{Key: "cdn", Label: "CDN", Provider: "test", Profile: "cdn", Enabled: true, LimitBytes: 1000, ExpiresAt: expires},
	}}
	requestJSON, _ := json.Marshal(request)
	w := makeRequest(h, "PUT", "/api/v1/accounts/site-user-42", string(requestJSON), testAPIKey)
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body.String())
	}
	var account accountResponse
	if err := json.Unmarshal(w.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	publicPath := strings.TrimPrefix(account.SubscriptionURL, "https://subscriptions.example")
	readSubscription := func() *httptest.ResponseRecorder { return makeRequest(h, "GET", publicPath+"?format=plain", "", "") }
	w = readSubscription()
	if w.Code != 200 || !strings.Contains(w.Body.String(), "%5BMAIN%5D") || !strings.Contains(w.Body.String(), "%5BCDN%5D") {
		t.Fatalf("merged %d %s", w.Code, w.Body.String())
	}
	main := provider.users["site-user-42_main"]
	main.UsedBytes = 1_000_000_000
	provider.users[main.ID] = main
	cdn := provider.users["site-user-42_cdn"]
	cdn.UsedBytes = 1000
	provider.users[cdn.ID] = cdn
	w = readSubscription()
	if w.Code != 200 || !strings.Contains(w.Body.String(), "%5BMAIN%5D") || strings.Contains(w.Body.String(), "%5BCDN%5D") {
		t.Fatalf("CDN exhaustion affected main: %d %s", w.Code, w.Body.String())
	}
	w = makeRequest(h, "GET", "/api/v1/accounts/site-user-42?refresh=true", "", testAPIKey)
	if err := json.Unmarshal(w.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	if account.Parts[0].Status != "active" || account.Parts[1].Status != "limited" || *account.Parts[1].RemainingBytes != 0 {
		t.Fatalf("statuses %s", w.Body.String())
	}
	request.Parts[0].ExpiresAt = time.Now().UTC().Add(-time.Hour)
	requestJSON, _ = json.Marshal(request)
	w = makeRequest(h, "PUT", "/api/v1/accounts/site-user-42", string(requestJSON), testAPIKey)
	if w.Code != 200 {
		t.Fatalf("expire %d %s", w.Code, w.Body.String())
	}
	w = readSubscription()
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("expired/exhausted account should have an empty subscription: %d %s", w.Code, w.Body.String())
	}
	w = makeRequest(h, "POST", "/api/v1/accounts/site-user-42/rotate-token", "", testAPIKey)
	if w.Code != 200 {
		t.Fatalf("rotate %d %s", w.Code, w.Body.String())
	}
	w = readSubscription()
	if w.Code != 404 {
		t.Fatalf("revoked token accepted: %d", w.Code)
	}
}
