package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

type slowProvider struct{ release chan struct{} }

func (p *slowProvider) Ensure(ctx context.Context, owner string, part bundle.Part) (bundle.Remote, error) {
	select {
	case <-p.release:
	case <-ctx.Done():
		return bundle.Remote{}, ctx.Err()
	}
	return bundle.Remote{ID: owner + part.Key, Username: part.Username, Status: "ACTIVE", LimitBytes: part.LimitBytes, ExpiresAt: part.ExpiresAt}, nil
}
func (*slowProvider) Read(context.Context, string) (bundle.Remote, error) {
	return bundle.Remote{}, bundle.ErrUnavailable
}
func (*slowProvider) Links(context.Context, string) ([]string, error) {
	return nil, bundle.ErrUnavailable
}

func TestHTTPAcceptedAfterFiveSecondsAndBackgroundCompletion(t *testing.T) {
	repo := bundle.NewMemoryRepository()
	p := &slowProvider{release: make(chan struct{})}
	s := bundle.NewService(repo, map[string]bundle.Provider{"slow": p})
	defer s.Close()
	h, err := New(s, repo, Config{APIKey: testAPIKey, PublicURL: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	req := bundle.PutRequest{Username: "async_user", Enabled: true, Parts: []bundle.PartRequest{{Key: "main", Provider: "slow", Profile: "main", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}}}
	data, _ := json.Marshal(req)
	started := time.Now()
	w := makeRequest(h, "PUT", "/api/v1/accounts/async", string(data), testAPIKey)
	if elapsed := time.Since(started); w.Code != 202 || elapsed < 4900*time.Millisecond || elapsed > 7*time.Second {
		t.Fatal("slow request was not accepted after bounded wait", w.Code, elapsed)
	}
	started = time.Now()
	if w := makeRequest(h, "GET", "/api/v1/accounts", "", testAPIKey); w.Code != 200 || time.Since(started) > time.Second {
		t.Fatal("list waited for the provider")
	}
	close(p.release)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		b, _ := s.Get(context.Background(), "async", false)
		if b.Parts[0].AppliedRevision == b.Revision && b.Parts[0].SyncStatus == "ready" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("request context cancellation stopped background work")
}
