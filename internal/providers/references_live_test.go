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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/aggregate"
	"github.com/saidovux/remna-quota/internal/httpapi"
	"github.com/saidovux/remna-quota/pkg/client"
)

type readOnlyTransport struct{ writes atomic.Int32 }

func (r *readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		r.writes.Add(1)
		return nil, errors.New("live aggregation attempted an upstream write")
	}
	return http.DefaultTransport.RoundTrip(req)
}

func TestLiveReadOnlyAggregation(t *testing.T) {
	if os.Getenv("REMNA_LIVE_READ_ONLY") != "true" {
		t.Skip("set REMNA_LIVE_READ_ONLY=true with two existing references")
	}
	var refs map[string]string
	if json.Unmarshal([]byte(os.Getenv("REMNA_LIVE_REFERENCES_JSON")), &refs) != nil || refs["main"] == "" || refs["cdn"] == "" || refs["main"] == refs["cdn"] {
		t.Fatal("two distinct existing references are required")
	}
	p, err := NewRemnawaveReferences(RemnaConfig{BaseURL: os.Getenv("REMNAWAVE_BASE_URL"), APIToken: os.Getenv("REMNAWAVE_API_TOKEN"), AllowHTTP: os.Getenv("REMNAWAVE_ALLOW_HTTP") == "true", ForwardedFor: os.Getenv("REMNAWAVE_FORWARDED_FOR"), ForwardedProto: os.Getenv("REMNAWAVE_FORWARDED_PROTO"), CaddyAPIKey: os.Getenv("REMNAWAVE_CADDY_API_KEY")})
	if err != nil {
		t.Fatal(err)
	}
	transport := &readOnlyTransport{}
	p.panel.client.Transport = transport
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if report := p.panel.CheckReferences(ctx); !report.Ready {
		t.Fatalf("read diagnostics failed: %+v", report.Checks)
	}
	repo := aggregate.NewMemoryRepository()
	service := aggregate.NewService(repo, map[string]aggregate.Provider{"remnawave": p})
	const apiKey = "live-aggregation-loopback-key-1234567890"
	h, err := httpapi.New(nil, repo, httpapi.Config{Aggregation: service, APIKey: apiKey, PublicURL: "https://read-test.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	api, err := client.New(server.URL, apiKey)
	if err != nil {
		t.Fatal(err)
	}
	request := client.PutBundleRequest{Enabled: true, ExternalRef: "any-application", Sources: []client.SourceReference{{Key: "main", Label: "MAIN", Provider: "remnawave", Reference: refs["main"], Enabled: true}, {Key: "cdn", Label: "CDN", Provider: "remnawave", Reference: refs["cdn"], Enabled: true}}}
	b, err := api.PutBundle(ctx, "read-only-test", request)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := api.GetBundle(ctx, b.ID, true)
	if err != nil || fresh.Status != "ready" || fresh.Stale || len(fresh.Sources) != 2 {
		t.Fatal("existing sources not observed")
	}
	fetch := func(link string) string {
		t.Helper()
		u, _ := url.Parse(link)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+u.Path+"?format=plain", nil)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal("loopback request failed")
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil || resp.StatusCode != 200 {
			t.Fatal("merged list unavailable")
		}
		body, _ := url.PathUnescape(string(data))
		return body
	}
	body := fetch(b.SubscriptionURL)
	if !strings.Contains(body, "[MAIN]") || !strings.Contains(body, "[CDN]") {
		t.Fatal("both real sources not in combined list")
	}
	b, err = api.RemoveSource(ctx, b.ID, "cdn", &b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	body = fetch(b.SubscriptionURL)
	if !strings.Contains(body, "[MAIN]") || strings.Contains(body, "[CDN]") {
		t.Fatal("source detachment failed")
	}
	if _, err := api.ResolveBundle(ctx, b.SubscriptionURL); err != nil {
		t.Fatal(err)
	}
	if err := api.DeleteBundle(ctx, b.ID, &b.Revision); err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		if _, err := p.Read(ctx, ref); err != nil {
			t.Fatal("upstream source unavailable after local deletion")
		}
	}
	if transport.writes.Load() != 0 {
		t.Fatal("upstream mutation attempted")
	}
	t.Log("two existing sources read and merged; CDN detached locally; bundle resolved/deleted; all upstream requests were GET")
}
