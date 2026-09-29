package remnawave

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/config"
)

func TestPublicClientHeadersAndUserContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "outer-key" {
			t.Errorf("X-Api-Key = %q", got)
		}
		if got := r.Header.Get("X-Forwarded-For"); got != "" {
			t.Errorf("unexpected forwarded header %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"id":42,"shortUuid":"short-1","username":"synthetic-user","status":"ACTIVE","trafficLimitBytes":0,"trafficLimitStrategy":"NO_RESET","expireAt":"2026-10-01T00:00:00Z","activeInternalSquads":[{"uuid":"11111111-1111-4111-8111-111111111111","name":"MAIN"}]}}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, "public_https")
	user, err := client.User(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !user.InSquad("11111111-1111-4111-8111-111111111111") {
		t.Fatal("expected user membership")
	}
}

func TestDockerBridgeHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-For") != "127.0.0.1" || r.Header.Get("X-Forwarded-Proto") != "https" {
			t.Error("missing bridge headers")
		}
		_, _ = w.Write([]byte(`{"response":{"version":"3.4.3","build":{"time":"x","number":"1"}}}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, "docker_bridge")
	if _, err := client.Metadata(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestUsageContractAndInclusiveDates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("start") != "2026-09-04" || r.URL.Query().Get("end") != "2026-09-05" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"response":{"days":[{"date":"2026-09-04","nodes":[{"uuid":"11111111-1111-4111-8111-111111111111","totalBytes":100}]},{"date":"2026-09-05","nodes":[{"uuid":"11111111-1111-4111-8111-111111111111","totalBytes":50}] }]}}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, "public_https")
	usage, err := client.SquadUserDailyUsage(context.Background(), "22222222-2222-4222-8222-222222222222", 42, time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC), time.Date(2026, 9, 5, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	total, err := usage.TotalBytes()
	if err != nil || total != 150 {
		t.Fatalf("total=%d err=%v", total, err)
	}
}

func TestMutationBodyAndAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		var body struct {
			UserIDs []int64 `json:"userIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.UserIDs) != 1 || body.UserIDs[0] != 42 {
			t.Errorf("body = %#v", body)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	client := testClient(t, server.URL, "public_https")
	if err := client.RemoveUsersFromSquad(context.Background(), "11111111-1111-4111-8111-111111111111", []int64{42}); err != nil {
		t.Fatal(err)
	}
}

func TestGETRetriesSelectedStatus(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"response":{"version":"3.4.3","build":{"time":"x","number":"1"}}}`))
	}))
	defer server.Close()
	client := testClient(t, server.URL, "public_https")
	if _, err := client.Metadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func testClient(t *testing.T, rawURL, mode string) *Client {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(config.Remnawave{BaseURL: u, APIToken: "test-token", CaddyAPIKey: "outer-key", AccessMode: mode, RequestTimeout: 2 * time.Second, ForwardedFor: "127.0.0.1", ForwardedProto: "https"})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
