package client_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/internal/httpapi"
	"github.com/saidovux/remna-quota/internal/providers"
	"github.com/saidovux/remna-quota/pkg/client"
)

// Exercise the public SDK against the actual handler, service, repository and
// demo adapter, including the unauthenticated URL a VPN application consumes.
func TestWebsiteSDKAndCombinedSubscription(t *testing.T) {
	const key = "integration-key-123456789012345678901234"
	repo := bundle.NewMemoryRepository()
	service := bundle.NewService(repo, map[string]bundle.Provider{"demo": providers.NewDemo()})
	handler, err := httpapi.New(service, repo, httpapi.Config{APIKey: key, PublicURL: "https://vpn.example"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	api, err := client.New(server.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(30 * 24 * time.Hour)
	request := client.PutAccountRequest{Username: "alice", Enabled: true, Parts: []client.PartRequest{
		{Key: "main", Label: "Main", Provider: "demo", Profile: "main", Enabled: true, ExpiresAt: expires},
		{Key: "cdn", Label: "CDN", Provider: "demo", Profile: "cdn", Enabled: true, LimitBytes: 1000, ExpiresAt: expires},
	}}
	account, err := api.PutAccount(context.Background(), "website-user-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(account.Parts) != 2 || account.SyncStatus != "ready" || account.Stale || account.Parts[0].Username != "alice_main" || account.Parts[1].Username != "alice_cdn" || !account.Parts[0].Unlimited || account.Parts[1].RemainingBytes == nil || *account.Parts[1].RemainingBytes != 1000 {
		t.Fatalf("website received incorrect independent quotas: %+v", account)
	}
	fetch := func(subscriptionURL string) (int, string) {
		t.Helper()
		u, err := url.Parse(subscriptionURL)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.Get(server.URL + u.Path + "?format=plain")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(data)
	}
	status, links := fetch(account.SubscriptionURL)
	if status != 200 || strings.Count(links, "vless://") != 2 {
		t.Fatalf("combined subscription not served: %d %s", status, links)
	}
	updated, err := api.GetAccount(context.Background(), account.ID, true)
	if err != nil || updated.Parts[1].UsedBytes == nil || *updated.Parts[1].UsedBytes != 0 {
		t.Fatalf("website refresh failed: %+v %v", updated, err)
	}
	request.Parts[1].Enabled = false
	account, err = api.PutAccount(context.Background(), account.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	status, links = fetch(account.SubscriptionURL)
	if status != 200 || strings.Count(links, "vless://") != 1 || strings.Contains(links, "alice_cdn") {
		t.Fatalf("disabled CDN leaked into combined list: %d %s", status, links)
	}
	oldURL := account.SubscriptionURL
	resolved, err := api.ResolveSubscription(context.Background(), oldURL)
	if err != nil || resolved.ID != account.ID || len(resolved.Parts) != 2 {
		t.Fatalf("website link login resolution failed: %v", err)
	}
	account, err = api.RotateToken(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := fetch(oldURL); status != 404 {
		t.Fatalf("old URL remains active: %d", status)
	}
	if _, err := api.ResolveSubscription(context.Background(), oldURL); err == nil {
		t.Fatal("SDK resolver accepted revoked link")
	}
	if status, _ := fetch(account.SubscriptionURL); status != 200 {
		t.Fatalf("replacement URL unavailable: %d", status)
	}
	request.Enabled = false
	if _, err := api.PutAccount(context.Background(), account.ID, request); err != nil {
		t.Fatal(err)
	}
	if status, _ := fetch(account.SubscriptionURL); status != 404 {
		t.Fatalf("disabled account URL remains active: %d", status)
	}
}
