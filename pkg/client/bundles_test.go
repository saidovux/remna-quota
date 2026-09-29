package client_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/saidovux/remna-quota/internal/aggregate"
	"github.com/saidovux/remna-quota/internal/httpapi"
	"github.com/saidovux/remna-quota/internal/providers"
	"github.com/saidovux/remna-quota/pkg/client"
)

func TestApplicationIndependentBundleAPI(t *testing.T) {
	ctx := context.Background()
	repo := aggregate.NewMemoryRepository()
	service := aggregate.NewService(repo, map[string]aggregate.Provider{"demo": providers.NewDemoReferences()})
	const key = "application-integration-test-key-123456789"
	h, err := httpapi.New(nil, repo, httpapi.Config{Aggregation: service, APIKey: key, PublicURL: "https://subs.example"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	api, err := client.New(server.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	caps, err := api.Capabilities(ctx)
	if err != nil || caps.ManagedAccountsEnabled || caps.MaxSources != 16 {
		t.Fatal("aggregation capabilities incorrect")
	}
	resp, err := http.Get(server.URL + "/api/v1/bundles")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("bundle inventory is not authenticated")
	}
	req := client.PutBundleRequest{Name: "Portable subscription", ExternalRef: "any-owner", Enabled: true, Sources: []client.SourceReference{{Key: "main", Label: "MAIN", Provider: "demo", Reference: "main", Enabled: true}}}
	b, err := api.PutBundle(ctx, "bot-subscription-1", req)
	if err != nil || b.Sources[0].Unlimited != nil {
		t.Fatal("creation inferred unknown quota or failed")
	}
	again, err := api.PutBundle(ctx, b.ID, req)
	if err != nil || again.Revision != b.Revision || again.SubscriptionURL != b.SubscriptionURL {
		t.Fatal("repeat changed resource identity")
	}
	if _, err := api.PutBundle(ctx, "site-subscription-2", req); err != nil {
		t.Fatal("second subscription for same external owner failed")
	}
	page, err := api.ListBundles(ctx, client.ListBundlesOptions{Limit: 1, ExternalRef: "any-owner"})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatal("first page incorrect")
	}
	page2, err := api.ListBundles(ctx, client.ListBundlesOptions{Limit: 1, ExternalRef: "any-owner", Cursor: page.NextCursor})
	if err != nil || len(page2.Items) != 1 || page2.Items[0].ID == page.Items[0].ID || page2.NextCursor != "" {
		t.Fatal("cursor pagination incorrect")
	}
	if _, err := api.ListBundles(ctx, client.ListBundlesOptions{Cursor: page.NextCursor, ExternalRef: "other-owner"}); err == nil {
		t.Fatal("cursor reused under different filter")
	}
	cdn := client.SourceReference{Key: "cdn", Label: "CDN", Provider: "demo", Reference: "cdn", Enabled: true}
	b, err = api.PutSource(ctx, b.ID, cdn, &b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := api.RefreshBundle(ctx, b.ID)
	if err != nil || fresh.Stale || fresh.Sources[0].Unlimited == nil || !*fresh.Sources[0].Unlimited || fresh.Sources[1].Unlimited == nil || *fresh.Sources[1].Unlimited {
		t.Fatal("independent quotas not exposed")
	}
	fetch := func(link string) (int, string) {
		t.Helper()
		u, _ := url.Parse(link)
		resp, err := http.Get(server.URL + u.Path + "?format=plain")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		decoded, _ := url.PathUnescape(string(data))
		return resp.StatusCode, decoded
	}
	if status, body := fetch(b.SubscriptionURL); status != 200 || !strings.Contains(body, "[MAIN]") || !strings.Contains(body, "[CDN]") {
		t.Fatal("constructor composition failed")
	}
	stale := int64(1)
	_, err = api.RemoveSource(ctx, b.ID, "cdn", &stale)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 || apiErr.Code != "revision_conflict" {
		t.Fatal("stale revision was not rejected")
	}
	b, err = api.RemoveSource(ctx, b.ID, "cdn", &b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := fetch(b.SubscriptionURL); status != 200 || strings.Contains(body, "[CDN]") {
		t.Fatal("CDN not removed")
	}
	resolved, err := api.ResolveBundle(ctx, b.SubscriptionURL)
	if err != nil || resolved.ID != b.ID {
		t.Fatal("generic URL resolution failed")
	}
	old := b.SubscriptionURL
	b, err = api.RotateBundleToken(ctx, b.ID, &b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := fetch(old); status != 404 {
		t.Fatal("old URL still works")
	}
	if _, err := api.ResolveBundle(ctx, old); err == nil {
		t.Fatal("old URL still resolves")
	}
	no := false
	b, err = api.PatchBundle(ctx, b.ID, client.PatchBundleRequest{Enabled: &no, ExpectedRevision: &b.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := fetch(b.SubscriptionURL); status != 404 {
		t.Fatal("disabled bundle still works")
	}
	if err := api.DeleteBundle(ctx, b.ID, &b.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetBundle(ctx, b.ID, false); err == nil {
		t.Fatal("deleted bundle found")
	}
	if _, err := api.GetBundle(ctx, "site-subscription-2", true); err != nil {
		t.Fatal("independent bundle was affected")
	}
	legacy, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/accounts/anything", nil)
	legacy.Header.Set("Authorization", "Bearer "+key)
	resp, err = server.Client().Do(legacy)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("managed API enabled without opt-in")
	}
}
