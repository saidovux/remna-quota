package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/internal/httpapi"
	"github.com/saidovux/remna-quota/pkg/client"
)

// This opt-in test creates two short-lived, uniquely named panel users. It
// deletes only records bearing this run's exact ownership marker. No connection
// credentials or subscription URLs are printed. It does not send VPN traffic.
func TestLiveRemnawaveBundleLifecycle(t *testing.T) {
	if os.Getenv("REMNA_LIVE_TEST") != "create-and-delete-test-users" {
		t.Skip("explicit live-panel opt-in required")
	}
	runID := os.Getenv("REMNA_LIVE_RUN_ID")
	if !regexp.MustCompile(`^[a-f0-9]{22}$`).MatchString(runID) {
		t.Fatal("REMNA_LIVE_RUN_ID must be 22 random hexadecimal characters")
	}
	var profiles map[string][]string
	if json.Unmarshal([]byte(os.Getenv("REMNAWAVE_PROFILES_JSON")), &profiles) != nil || len(profiles["main"]) == 0 || len(profiles["cdn"]) == 0 {
		t.Fatal("main and cdn profiles are required")
	}
	p, err := NewRemnawave(RemnaConfig{
		BaseURL: os.Getenv("REMNAWAVE_BASE_URL"), APIToken: os.Getenv("REMNAWAVE_API_TOKEN"),
		Profiles: profiles, AllowHTTP: os.Getenv("REMNAWAVE_ALLOW_HTTP") == "true",
		ForwardedFor: os.Getenv("REMNAWAVE_FORWARDED_FOR"), ForwardedProto: os.Getenv("REMNAWAVE_FORWARDED_PROTO"),
		CaddyAPIKey: os.Getenv("REMNAWAVE_CADDY_API_KEY"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if report := p.Check(ctx); !report.Ready {
		t.Fatalf("read probes failed: %+v", report.Checks)
	}
	owner, username := "live-"+runID, "rqlive_"+runID
	for _, key := range []string{"main", "cdn"} {
		_, err := p.getUser(ctx, "/api/users/by-username/"+username+"_"+key)
		if !errors.Is(err, bundle.ErrNotFound) {
			t.Fatal("test username is not confirmed absent")
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			user, err := p.getUser(cleanupCtx, "/api/users/by-username/"+username+"_"+key)
			if errors.Is(err, bundle.ErrNotFound) {
				return
			}
			if err != nil || user.Description != ownership(owner, key) || user.Username != username+"_"+key {
				t.Error("cleanup could not verify test user ownership")
				return
			}
			endpoint := *p.baseURL
			endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/users/" + strconv.FormatInt(user.ID, 10)
			req, _ := http.NewRequestWithContext(cleanupCtx, http.MethodDelete, endpoint.String(), nil)
			req.Header.Set("Authorization", "Bearer "+p.token)
			for name, value := range map[string]string{"X-Forwarded-For": p.forwardedFor, "X-Forwarded-Proto": p.forwardedProto, "X-Api-Key": p.caddyAPIKey} {
				if value != "" {
					req.Header.Set(name, value)
				}
			}
			resp, err := p.client.Do(req)
			if err != nil {
				t.Error("test user cleanup request failed")
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
				t.Errorf("test user cleanup returned HTTP %d", resp.StatusCode)
			}
			if _, err := p.getUser(cleanupCtx, "/api/users/by-username/"+username+"_"+key); !errors.Is(err, bundle.ErrNotFound) {
				t.Error("test user removal not confirmed")
			}
		})
	}
	repo := bundle.NewMemoryRepository()
	service := bundle.NewService(repo, map[string]bundle.Provider{"remnawave": p})
	const apiKey = "live-test-loopback-key-12345678901234567890"
	h, err := httpapi.New(service, repo, httpapi.Config{APIKey: apiKey, PublicURL: "https://quota-test.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	api, err := client.New(server.URL, apiKey, client.WithTimeout(90*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	wanted := client.PutAccountRequest{Username: username, Enabled: true, Parts: []client.PartRequest{
		{Key: "main", Label: "MAIN", Provider: "remnawave", Profile: "main", Enabled: true, ExpiresAt: expires, ResetStrategy: "NO_RESET"},
		{Key: "cdn", Label: "CDN", Provider: "remnawave", Profile: "cdn", Enabled: true, LimitBytes: 1 << 20, ExpiresAt: expires, ResetStrategy: "NO_RESET"},
	}}
	put := func(stage string) client.Account {
		t.Helper()
		a, err := api.PutAccount(ctx, owner, wanted)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		if a.SyncStatus != "ready" || len(a.Parts) != 2 {
			for _, part := range a.Parts {
				t.Logf("%s: part=%s sync=%s error=%s", stage, part.Key, part.SyncStatus, part.LastError)
			}
			t.Fatalf("%s: account did not synchronize", stage)
		}
		return a
	}
	a := put("create")
	page, err := api.ListAccounts(ctx, client.ListAccountsOptions{Search: username})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != owner || page.Items[0].ActiveParts != 2 {
		t.Fatal("managed directory did not expose synchronized account")
	}
	if a.Parts[1].ObservedTotalBytes == nil || a.Parts[1].ProviderLimitBytes == nil || *a.Parts[1].ProviderLimitBytes != 1<<20 {
		t.Fatal("managed traffic fields missing")
	}
	if a.Parts[0].ProviderUserID == a.Parts[1].ProviderUserID || !a.Parts[0].Unlimited || a.Parts[1].LimitBytes != 1<<20 {
		t.Fatal("parts do not have independent users and limits")
	}
	again := put("retry")
	if again.Parts[0].ProviderUserID != a.Parts[0].ProviderUserID || again.Parts[1].ProviderUserID != a.Parts[1].ProviderUserID {
		t.Fatal("retry changed provider identities")
	}
	fetch := func(link string) (int, string) {
		t.Helper()
		u, _ := url.Parse(link)
		r, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+u.Path+"?format=plain", nil)
		resp, err := server.Client().Do(r)
		if err != nil {
			t.Fatal("loopback subscription request failed")
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		if err != nil {
			t.Fatal("reading merged subscription failed")
		}
		decoded, _ := url.PathUnescape(string(body))
		return resp.StatusCode, decoded
	}
	if status, body := fetch(a.SubscriptionURL); status != 200 || !strings.Contains(body, "[MAIN]") || !strings.Contains(body, "[CDN]") {
		t.Fatalf("merged subscription is missing either profile (HTTP %d)", status)
	}
	wanted.Parts[1].Enabled = false
	a = put("disable CDN")
	if a.Parts[0].Status != "active" || a.Parts[1].Status != "disabled" {
		t.Fatal("CDN disable affected the wrong part")
	}
	if status, body := fetch(a.SubscriptionURL); status != 200 || !strings.Contains(body, "[MAIN]") || strings.Contains(body, "[CDN]") {
		t.Fatalf("disabled CDN was not omitted independently (HTTP %d)", status)
	}
	wanted.Parts[1].Enabled = true
	wanted.Parts[1].LimitBytes = 2 << 20
	wanted.Parts[1].ExpiresAt = expires.Add(time.Hour)
	a = put("enable and extend CDN")
	if a.Parts[1].Status != "active" || a.Parts[1].LimitBytes != 2<<20 || !a.Parts[0].ExpiresAt.Equal(expires) {
		t.Fatal("independent quota/expiration update failed")
	}
	if resolved, err := api.ResolveSubscription(ctx, a.SubscriptionURL); err != nil || resolved.ID != owner {
		t.Fatal("website link resolver failed")
	}
	oldURL := a.SubscriptionURL
	if _, err := api.RotateToken(ctx, owner); err != nil {
		t.Fatal("token rotation failed")
	}
	if _, err := api.ResolveSubscription(ctx, oldURL); err == nil {
		t.Fatal("revoked link resolved")
	}
	if status, _ := fetch(oldURL); status != 404 {
		t.Fatal("revoked subscription URL remained accessible")
	}
	t.Log("create, retry, merge, independent disable/enable, quota/expiry update, resolver and token rotation passed")
}
