package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/saidovux/remna-quota/internal/aggregate"
	"github.com/saidovux/remna-quota/internal/providers"
)

func TestAggregationRejectsBusinessFieldsAndUnsafeReferences(t *testing.T) {
	repo := aggregate.NewMemoryRepository()
	service := aggregate.NewService(repo, map[string]aggregate.Provider{"demo": providers.NewDemoReferences()})
	h, err := New(nil, repo, Config{Aggregation: service, APIKey: testAPIKey, PublicURL: "https://subs.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"enabled":true,"sources":[],"price":150}`, 400},
		{`{"enabled":true,"sources":[],"customer_id":"site-only"}`, 400},
		{`{"sources":[]}`, 400},
		{`{"enabled":true}`, 400},
		{`{"enabled":true,"sources":[{"key":"main","provider":"demo","reference":"main"}]}`, 400},
		{`{"enabled":true,"sources":[{"key":"main","provider":"demo","reference":"http://127.0.0.1/private","enabled":true}]}`, 422},
		{`{"enabled":true,"sources":[],"expected_revision":-1}`, 422},
		{`{"enabled":true,"sources":[]} {}`, 400},
		{`{"enabled":true,"sources":[],"name":"` + strings.Repeat("x", 65536) + `"}`, 413},
	} {
		if got := makeRequest(h, http.MethodPut, "/api/v1/bundles/portable", tc.body, testAPIKey).Code; got != tc.status {
			t.Errorf("expected %d, got %d", tc.status, got)
		}
	}
	if got := makeRequest(h, http.MethodPut, "/api/v1/bundles/portable", `{"enabled":true,"sources":[]}`, testAPIKey).Code; got != 200 {
		t.Fatal("empty constructor bundle not accepted")
	}
	if got := makeRequest(h, http.MethodPost, "/api/v1/bundles/resolve", `{"subscription_url":"https://attacker.invalid/sub/secret"}`, testAPIKey).Code; got != 400 {
		t.Fatal("resolver accepted external origin")
	}
	if got := makeRequest(h, http.MethodDelete, "/api/v1/bundles/portable?expected_revision=-1", "", testAPIKey).Code; got != 400 {
		t.Fatal("negative delete revision accepted")
	}
}
