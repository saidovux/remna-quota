package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

const testAPIKey = "test-api-key-01234567890123456789012345"

type stubService struct {
	b         bundle.Bundle
	err       error
	links     []string
	linksErr  error
	putCount  int
	refreshed bool
}

func (s *stubService) Put(_ context.Context, _ string, _ bundle.PutRequest) (bundle.Bundle, error) {
	s.putCount++
	return s.b, s.err
}
func (s *stubService) Get(_ context.Context, _ string, refresh bool) (bundle.Bundle, error) {
	s.refreshed = refresh
	return s.b, s.err
}
func (s *stubService) ByToken(_ context.Context, token string) (bundle.Bundle, error) {
	if token != s.b.Token || !s.b.Enabled {
		return bundle.Bundle{}, bundle.ErrNotFound
	}
	return s.b, s.err
}
func (s *stubService) RotateToken(_ context.Context, _ string) (bundle.Bundle, error) {
	s.b.Token = "rotated-public-token"
	return s.b, s.err
}
func (s *stubService) Sync(context.Context, string) (bundle.Bundle, error) { return s.b, s.err }
func (s *stubService) Links(context.Context, bundle.Bundle) ([]string, error) {
	return s.links, s.linksErr
}

func (s *stubService) ConfigForJSON(context.Context, bundle.Bundle) ([]byte, error) {
	return []byte(`{"outbounds":[]}`), s.err
}

type stubHealth struct{ err error }

func (s stubHealth) Ping(context.Context) error { return s.err }

func newTestHandler(t *testing.T, s *stubService) http.Handler {
	t.Helper()
	h, err := New(s, stubHealth{}, Config{APIKey: testAPIKey, PublicURL: "https://subscriptions.example"})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func makeRequest(h http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func testBundle() bundle.Bundle {
	expiry := time.Now().Add(24 * time.Hour)
	return bundle.Bundle{ID: "account-1", Username: "alice", Enabled: true, Token: "public-token", Parts: []bundle.Part{
		{PartRequest: bundle.PartRequest{Key: "main", Label: "MAIN", Provider: "remnawave", Profile: "main", Enabled: true, ExpiresAt: expiry}, Username: "alice_main", SyncStatus: "ready", Remote: &bundle.Remote{ID: "main-remote", Status: "ACTIVE", UsedBytes: 900, LimitBytes: 0, ExpiresAt: expiry}},
		{PartRequest: bundle.PartRequest{Key: "cdn", Label: "CDN", Provider: "remnawave", Profile: "cdn", Enabled: true, LimitBytes: 1000, ExpiresAt: expiry}, Username: "alice_cdn", SyncStatus: "ready", Remote: &bundle.Remote{ID: "cdn-remote", Status: "ACTIVE", UsedBytes: 300, LimitBytes: 1000, ExpiresAt: expiry}},
	}}
}

func TestAdminAuthAndIndependentTraffic(t *testing.T) {
	s := &stubService{b: testBundle()}
	h := newTestHandler(t, s)
	for _, key := range []string{"", "wrong"} {
		w := makeRequest(h, "GET", "/api/v1/accounts/account-1", "", key)
		if w.Code != 401 || w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("unauthorized %d %s", w.Code, w.Body.String())
		}
	}
	w := makeRequest(h, "GET", "/api/v1/accounts/account-1?refresh=true", "", testAPIKey)
	if w.Code != 200 || !s.refreshed {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	var body accountResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SubscriptionURL != "https://subscriptions.example/sub/public-token" || len(body.Parts) != 2 {
		t.Fatalf("unexpected account %#v", body)
	}
	if !body.Parts[0].Unlimited || body.Parts[0].RemainingBytes != nil || *body.Parts[0].UsedBytes != 900 {
		t.Fatalf("unlimited pool %#v", body.Parts[0])
	}
	if body.Parts[1].Unlimited || *body.Parts[1].RemainingBytes != 700 {
		t.Fatalf("limited pool %#v", body.Parts[1])
	}
	if strings.Contains(w.Body.String(), "token_hash") || strings.Contains(w.Body.String(), `"token":`) {
		t.Fatal("private token fields exposed")
	}
	if w.Header().Get("Cache-Control") != "no-store, max-age=0" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unsafe headers %v", w.Header())
	}
}

func TestStrictBodyParsing(t *testing.T) {
	s := &stubService{b: testBundle()}
	h := newTestHandler(t, s)
	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{"unknown", `{"username":"alice","unexpected":true}`, 400},
		{"trailing", `{"username":"alice"} {}`, 400},
		{"malformed", `{"username":`, 400},
		{"array", `[]`, 400},
		{"missing enabled", `{"username":"alice","parts":[]}`, 400},
		{"missing limit", `{"username":"alice","enabled":true,"parts":[{"key":"main","enabled":true}]}`, 400},
		{"null limit", `{"username":"alice","enabled":true,"parts":[{"key":"main","enabled":true,"limit_bytes":null}]}`, 400},
		{"missing part enabled", `{"username":"alice","enabled":true,"parts":[{"key":"main","limit_bytes":0}]}`, 400},
		{"oversized", `{"username":"` + strings.Repeat("a", 65536) + `"}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := makeRequest(h, "PUT", "/api/v1/accounts/account-1", tc.body, testAPIKey)
			if w.Code != tc.code {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	if s.putCount != 0 {
		t.Fatal("malformed input reached service")
	}
	w := makeRequest(h, "PUT", "/api/v1/accounts/account-1", `{"username":"alice","enabled":true,"parts":[]}`, testAPIKey)
	if w.Code != 200 || s.putCount != 1 {
		t.Fatalf("valid request: %d %s", w.Code, w.Body.String())
	}
}

func TestOldSnapshotIsStaleWithoutPendingMutation(t *testing.T) {
	b := testBundle()
	old := time.Now().Add(-10 * time.Minute)
	for i := range b.Parts {
		b.Parts[i].SyncedAt = &old
	}
	h := newTestHandler(t, &stubService{b: b})
	w := makeRequest(h, "GET", "/api/v1/accounts/account-1", "", testAPIKey)
	var response accountResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Stale || !response.Parts[0].Stale || response.SyncStatus != "ready" {
		t.Fatalf("incorrect old snapshot state: %s", w.Body.String())
	}
}

func TestDesiredExpiryWinsWhenPanelCannotStorePastDates(t *testing.T) {
	b := testBundle()
	b.Parts[1].ExpiresAt = time.Now().Add(-time.Hour)
	b.Parts[1].Remote.Status = "DISABLED"
	h := newTestHandler(t, &stubService{b: b})
	w := makeRequest(h, "GET", "/api/v1/accounts/account-1", "", testAPIKey)
	var response accountResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Parts[1].Status != "expired" || response.Parts[0].Status != "active" {
		t.Fatalf("incorrect expiry: %s", w.Body.String())
	}
}

func TestPendingMutationAndStaleEffectiveLimits(t *testing.T) {
	s := &stubService{b: testBundle()}
	s.b.Parts[1].SyncStatus = "error"
	s.b.Parts[1].LastError = "provider_unavailable"
	s.b.Parts[1].LimitBytes = 500
	h := newTestHandler(t, s)
	w := makeRequest(h, "POST", "/api/v1/accounts/account-1/sync", "", testAPIKey)
	var response accountResponse
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if w.Code != 202 || !response.Stale || response.SyncStatus != "error" || response.Parts[1].LimitBytes != 1000 || response.Parts[1].DesiredLimitBytes != 500 {
		t.Fatalf("pending result %d %s", w.Code, w.Body.String())
	}
}

func TestPublicSubscriptionAndRotation(t *testing.T) {
	s := &stubService{b: testBundle(), links: []string{"vless://a@main.example:443#%5BMAIN%5D", "vless://b@cdn.example:443#%5BCDN%5D"}}
	h := newTestHandler(t, s)
	w := makeRequest(h, "GET", "/sub/public-token", "", "")
	decoded, err := base64.StdEncoding.DecodeString(w.Body.String())
	if w.Code != 200 || err != nil || strings.Count(string(decoded), "vless://") != 2 {
		t.Fatalf("base64 subscription %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Subscription-Userinfo") != "" {
		t.Fatal("mixed pools cannot have summed traffic metadata")
	}
	w = makeRequest(h, "GET", "/sub/public-token?format=plain", "", "")
	if w.Code != 200 || w.Body.String() != string(decoded) {
		t.Fatalf("plain subscription %d %s", w.Code, w.Body.String())
	}
	w = makeRequest(h, "HEAD", "/sub/public-token", "", "")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("HEAD subscription %d %s", w.Code, w.Body.String())
	}
	w = makeRequest(h, "POST", "/api/v1/accounts/account-1/rotate-token", "", testAPIKey)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "/sub/rotated-public-token") {
		t.Fatalf("rotation %d %s", w.Code, w.Body.String())
	}
	if w = makeRequest(h, "GET", "/sub/public-token", "", ""); w.Code != 404 {
		t.Fatalf("old token remains valid %d", w.Code)
	}
	if w = makeRequest(h, "GET", "/sub/rotated-public-token", "", ""); w.Code != 200 {
		t.Fatalf("new token invalid %d", w.Code)
	}
	s.b.Enabled = false
	if w = makeRequest(h, "GET", "/sub/rotated-public-token", "", ""); w.Code != 404 {
		t.Fatalf("disabled account accessible %d", w.Code)
	}
}

func TestPublicProviderFailureDoesNotReturnPartialSubscription(t *testing.T) {
	s := &stubService{b: testBundle(), links: []string{"vless://a@main.example:443"}, linksErr: errors.Join(bundle.ErrUnavailable, errors.New("secret-panel-api-key"))}
	h := newTestHandler(t, s)
	w := makeRequest(h, "GET", "/sub/public-token", "", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "vless") {
		t.Fatalf("leaked partial response %d %s", w.Code, w.Body.String())
	}
	s.linksErr = nil
	s.links = []string{"https://untrusted.example/subscription"}
	w = makeRequest(h, "GET", "/sub/public-token", "", "")
	if w.Code != 503 {
		t.Fatalf("malformed link accepted %d", w.Code)
	}
}

func TestRoutesFormatsAndErrors(t *testing.T) {
	s := &stubService{b: testBundle()}
	h := newTestHandler(t, s)
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"GET", "/healthz", 200}, {"GET", "/readyz", 200}, {"POST", "/healthz", 405},
		{"GET", "/unknown", 404}, {"GET", "/api/v1/missing", 404}, {"GET", "/api/v1/accounts/a/b/c", 404},
		{"DELETE", "/api/v1/accounts/account-1", 405}, {"GET", "/api/v1/accounts/account-1/sync", 405},
		{"GET", "/api/v1/accounts/account-1?refresh=maybe", 400}, {"GET", "/sub/public-token?format=clash", 400},
		{"GET", "/sub/other-users-token", 404}, {"GET", "/sub/", 404}, {"POST", "/sub/public-token", 405},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := makeRequest(h, tc.method, tc.path, "", testAPIKey)
			if w.Code != tc.code {
				t.Fatalf("got %d want %d: %s", w.Code, tc.code, w.Body.String())
			}
		})
	}
	for _, tc := range []struct {
		err  error
		code int
	}{{bundle.ErrNotFound, 404}, {bundle.ErrConflict, 409}, {bundle.ErrInvalid, 422}, {bundle.ErrUnavailable, 503}, {errors.New("secret backend failure"), 500}} {
		s.err = tc.err
		w := makeRequest(h, "GET", "/api/v1/accounts/account-1", "", testAPIKey)
		if w.Code != tc.code || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("error mapping %d %s", w.Code, w.Body.String())
		}
	}
}

func TestHealthAndInvalidConfiguration(t *testing.T) {
	s := &stubService{b: testBundle()}
	h, err := New(s, stubHealth{errors.New("database secret")}, Config{APIKey: testAPIKey, PublicURL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	w := makeRequest(h, "GET", "/readyz", "", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("readiness %d %s", w.Code, w.Body.String())
	}
	for _, config := range []Config{{APIKey: "short", PublicURL: "https://example.com"}, {APIKey: testAPIKey, PublicURL: "//example.com"}, {APIKey: testAPIKey, PublicURL: "https://user:password@example.com"}, {APIKey: testAPIKey, PublicURL: "https://example.com?query=value"}} {
		if _, err := New(s, stubHealth{}, config); err == nil {
			t.Fatalf("invalid configuration accepted %#v", config)
		}
	}
}
