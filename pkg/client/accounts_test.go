package client_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/internal/httpapi"
	"github.com/saidovux/remna-quota/internal/providers"
	"github.com/saidovux/remna-quota/pkg/client"
)

func TestManagedDirectoryAndIndependentPartUpdates(t *testing.T) {
	const key = "integration-key-123456789012345678901234"
	ctx := context.Background()
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
	caps, err := api.Capabilities(ctx)
	if err != nil || !caps.ManagedAccountsEnabled || caps.MaxParts != 8 || len(caps.ManagedProviders) != 1 || len(caps.ManagedProviders[0].Profiles) != 2 {
		t.Fatalf("managed capabilities: %v", err)
	}
	req := client.PutAccountRequest{Username: "alice", Name: "Phone", ExternalRef: "customer-1", Enabled: true, Parts: []client.PartRequest{{Key: "main", Label: "MAIN", Provider: "demo", Profile: "main", Enabled: true, ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}}}
	first, err := api.PutAccount(ctx, "subscription-1", req)
	if err != nil {
		t.Fatal(err)
	}
	req.Username = "alice_second"
	_, err = api.PutAccount(ctx, "subscription-2", req)
	if err != nil {
		t.Fatal(err)
	}
	page, err := api.ListAccounts(ctx, client.ListAccountsOptions{Limit: 1, ExternalRef: "customer-1", Search: "alice"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.NextCursor == "" {
		t.Fatalf("first page: %v", err)
	}
	page, err = api.ListAccounts(ctx, client.ListAccountsOptions{Limit: 1, Cursor: page.NextCursor, ExternalRef: "customer-1", Search: "alice"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "subscription-2" || page.NextCursor != "" {
		t.Fatalf("next page: %v", err)
	}
	cdn := client.PartRequest{Key: "cdn", Provider: "demo", Profile: "cdn", Label: "CDN", Enabled: true, LimitBytes: 70 << 30, ExpiresAt: req.Parts[0].ExpiresAt}
	updated, err := api.PutPart(ctx, first.ID, cdn, &first.Revision)
	if err != nil || len(updated.Parts) != 2 || updated.Parts[0].Username != "alice_main" || updated.Revision != 2 || updated.Status != "active" || updated.ActiveParts != 2 || updated.SubscriptionURL != first.SubscriptionURL {
		t.Fatalf("part append: %v", err)
	}
	if _, err := api.PutPart(ctx, first.ID, cdn, &first.Revision); err == nil {
		t.Fatal("stale revision accepted")
	} else {
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != "revision_conflict" {
			t.Fatalf("wrong revision response: %v", err)
		}
	}
	cdn.Enabled = false
	updated, err = api.PutPart(ctx, first.ID, cdn, nil)
	if err != nil || updated.Status != "partial" || updated.ActiveParts != 1 || updated.Parts[0].Status != "active" || updated.Parts[1].Status != "disabled" {
		t.Fatalf("independent CDN update: %v", err)
	}
	off := false
	updated, err = api.PatchAccount(ctx, first.ID, client.PatchAccountRequest{Enabled: &off, ExpectedRevision: &updated.Revision})
	if err != nil || updated.Status != "disabled" || updated.ActiveParts != 0 {
		t.Fatalf("account disable: %v", err)
	}
	page, err = api.ListAccounts(ctx, client.ListAccountsOptions{Enabled: &off})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("disabled directory: %v", err)
	}
}
