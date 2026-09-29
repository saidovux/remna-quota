package backend

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	quota "github.com/saidovux/remna-quota/pkg/client"
)

// Starts the real runtime in-process, with its worker and a TCP HTTP listener;
// cancellation exercises graceful shutdown without detached helper processes.
func TestDemoRuntimeLifecycle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	base := "http://" + addr
	key := "runtime-test-key-at-least-32-characters"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{EnableManagedAccounts: true, ListenAddr: addr, PublicURL: base, APIKey: key, Provider: "demo", SyncInterval: time.Minute})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("runtime did not shut down")
		}
	}()
	httpClient := &http.Client{Timeout: time.Second}
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		resp, err := httpClient.Get(base + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("runtime did not become ready")
	}
	api, err := quota.New(base, key)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().UTC().Add(24 * time.Hour)
	req := quota.PutAccountRequest{Username: "runtime", Enabled: true, Parts: []quota.PartRequest{
		{Key: "main", Label: "MAIN", Provider: "demo", Profile: "main", Enabled: true, ExpiresAt: expiry},
		{Key: "cdn", Label: "CDN", Provider: "demo", Profile: "cdn", Enabled: true, ExpiresAt: expiry, LimitBytes: 50 * 1024 * 1024 * 1024},
	}}
	account, err := api.PutAccount(ctx, "runtime-user", req)
	if err != nil || account.SyncStatus != "ready" || len(account.Parts) != 2 {
		t.Fatalf("create: %+v %v", account, err)
	}
	diagnostics, err := api.Diagnostics(ctx)
	if err != nil || diagnostics.Accounts != 1 || diagnostics.PendingAccounts != 0 || account.Parts[0].ObservedAt == nil || account.Parts[0].AppliedRevision != account.Revision || account.Parts[0].AccountingStatus != "ok" {
		t.Fatal("SDK diagnostics or synchronization fields missing", err)
	}
	again, err := api.PutAccount(ctx, "runtime-user", req)
	if err != nil || account.Revision != again.Revision || account.SubscriptionURL != again.SubscriptionURL {
		t.Fatal("PUT lost idempotency")
	}
	resp, err := httpClient.Get(account.SubscriptionURL)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	plain, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil || strings.Count(string(plain), "vless://") != 2 {
		t.Fatal("two-part subscription not merged")
	}
	rotated, err := api.RotateToken(ctx, "runtime-user")
	if err != nil {
		t.Fatal(err)
	}
	resp, err = httpClient.Get(account.SubscriptionURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("old URL was not revoked")
	}
	req.Parts[1].Enabled = false
	if _, err = api.PutAccount(ctx, "runtime-user", req); err != nil {
		t.Fatal(err)
	}
	resp, err = httpClient.Get(rotated.SubscriptionURL + "?format=plain")
	if err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || strings.Count(string(data), "vless://") != 1 || !strings.Contains(string(data), "MAIN") {
		t.Fatal("CDN disable affected MAIN")
	}
}
