package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testKey = "test-key-123456789012345678901234567890"

func TestClientMethodsAndDegradedAcceptedResponse(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Error("missing server API credential")
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Error("missing JSON accept")
		}
		switch calls {
		case 1:
			if r.Method != "PUT" || r.URL.Path != "/prefix/api/v1/accounts/user-42" || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("incorrect PUT: %s %s", r.Method, r.URL)
			}
			var request PutAccountRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Username != "alice" || request.Parts[0].LimitBytes != 1000 {
				t.Errorf("wrong request: %+v %v", request, err)
			}
			w.WriteHeader(http.StatusAccepted)
		case 2:
			if r.Method != "GET" || r.URL.RawQuery != "refresh=true" {
				t.Errorf("incorrect refresh: %s %s", r.Method, r.URL)
			}
		case 3:
			if r.Method != "POST" || r.URL.Path != "/prefix/api/v1/accounts/user-42/sync" {
				t.Errorf("incorrect sync: %s %s", r.Method, r.URL)
			}
		case 4:
			if r.Method != "POST" || r.URL.Path != "/prefix/api/v1/accounts/user-42/rotate-token" {
				t.Errorf("incorrect rotation: %s %s", r.Method, r.URL)
			}
		}
		fmt.Fprint(w, `{"id":"user-42","username":"alice","sync_status":"pending","stale":true,"parts":[{"key":"cdn","used_bytes":null,"remaining_bytes":null,"desired_limit_bytes":1000}]}`)
	}))
	defer server.Close()
	c, err := New(server.URL+"/prefix/", testKey)
	if err != nil {
		t.Fatal(err)
	}
	account, err := c.PutAccount(context.Background(), "user-42", PutAccountRequest{Username: "alice", Parts: []PartRequest{{Key: "cdn", LimitBytes: 1000}}})
	if err != nil || account.SyncStatus != "pending" || !account.Stale || account.Parts[0].UsedBytes != nil || account.Parts[0].DesiredLimitBytes != 1000 {
		t.Fatalf("202 account not decoded: %+v %v", account, err)
	}
	if _, err := c.GetAccount(context.Background(), "user-42", true); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SyncAccount(context.Background(), "user-42"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RotateToken(context.Background(), "user-42"); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestClientNeverFollowsRedirectWithCredential(t *testing.T) {
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	supplied := &http.Client{Timeout: time.Hour}
	c, err := New(server.URL, testKey, WithHTTPClient(supplied))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.GetAccount(context.Background(), "alice", false)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTemporaryRedirect || redirected {
		t.Fatalf("unsafe redirect behavior: redirected=%v err=%v", redirected, err)
	}
	if supplied.CheckRedirect != nil || supplied.Timeout != time.Hour {
		t.Fatal("SDK mutated caller's HTTP client")
	}
}

func TestAPIErrorScrubsUpstreamBody(t *testing.T) {
	for _, code := range []string{"account_conflict", testKey} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(409)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "secret": testKey})
			}))
			defer server.Close()
			c, _ := New(server.URL, testKey)
			_, err := c.GetAccount(context.Background(), "alice", false)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 || strings.Contains(err.Error(), testKey) {
				t.Fatalf("unsafe API error: %v", err)
			}
			if code == "account_conflict" && apiErr.Code != code {
				t.Fatal("lost safe machine error code")
			}
		})
	}
}

func TestInvalidAccountIDsNeverReachServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid account id reached network") }))
	defer server.Close()
	c, _ := New(server.URL, testKey)
	for _, id := range []string{"", "../secret", "alice/sync", "alice?refresh=true", "alice%2fsecret", strings.Repeat("a", 81)} {
		if _, err := c.GetAccount(context.Background(), id, true); !errors.Is(err, ErrInvalidID) {
			t.Errorf("id %q accepted: %v", id, err)
		}
	}
}

func TestResponseValidationAndSizeCap(t *testing.T) {
	for _, data := range []string{`{}`, `{"id":"other","username":"alice","sync_status":"ready"}`, `{"id":"alice","username":"alice","sync_status":"ready"} {}`, strings.Repeat("x", maxResponseBytes+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, data) }))
		c, _ := New(server.URL, testKey)
		if _, err := c.GetAccount(context.Background(), "alice", false); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("bad response accepted: %v", err)
		}
		server.Close()
	}
}

func TestConfigurationAndCancellation(t *testing.T) {
	for _, base := range []string{"relative", "ftp://host", "https://user:password@host", "https://host?key=secret", "https://host#secret"} {
		if _, err := New(base, testKey); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("invalid configuration accepted: %q", base)
		}
	}
	if _, err := New("http://localhost", "short"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("short key accepted")
	}
	if _, err := New("http://localhost", testKey, WithTimeout(0)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("unbounded timeout accepted")
	}
	c, err := New("http://localhost:1", testKey, WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetAccount(ctx, "alice", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("context lost: %v", err)
	}
}
