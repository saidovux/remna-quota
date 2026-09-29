package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/internal/providers"
)

func TestResolveCombinedSubscriptionForWebsiteSession(t *testing.T) {
	repo := bundle.NewMemoryRepository()
	service := bundle.NewService(repo, map[string]bundle.Provider{"demo": providers.NewDemo()})
	h, err := New(service, repo, Config{APIKey: testAPIKey, PublicURL: "https://subs.example"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := service.Put(context.Background(), "website-user", bundle.PutRequest{Username: "alice", Enabled: true, Parts: []bundle.PartRequest{{Key: "main", Provider: "demo", Profile: "main", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	link := "https://subs.example/sub/" + b.Token
	post := func(link, key string) int {
		body, _ := json.Marshal(map[string]string{"subscription_url": link})
		return makeRequest(h, http.MethodPost, "/api/v1/subscriptions/resolve", string(body), key).Code
	}
	if got := post(link, ""); got != 401 {
		t.Fatalf("resolver is not protected: %d", got)
	}
	if got := post(link, testAPIKey); got != 200 {
		t.Fatalf("valid combined link refused: %d", got)
	}
	for _, invalid := range []string{
		strings.Replace(link, "subs.example", "127.0.0.1", 1),
		strings.Replace(link, "subs.example", "subs.example.attacker.invalid", 1),
		strings.Replace(link, "https://", "http://", 1),
		strings.Replace(link, "https://", "https://user:password@", 1),
		link + "?format=plain", link + "#fragment", link + "/extra", link + "?",
		"https://subs.example/sub/%41" + b.Token[1:],
	} {
		if got := post(invalid, testAPIKey); got != 400 {
			t.Errorf("invalid URL accepted (status %d)", got)
		}
	}
	if _, err := service.RotateToken(context.Background(), b.ID); err != nil {
		t.Fatal(err)
	}
	if got := post(link, testAPIKey); got != 404 {
		t.Fatalf("rotated token accepted for login: %d", got)
	}
}
