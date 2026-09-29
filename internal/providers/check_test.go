package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckDetectsProductionScopeGapWithoutWrites(t *testing.T) {
	for _, denied := range []int{0, http.StatusForbidden, http.StatusUnauthorized} {
		t.Run(http.StatusText(denied), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("check attempted mutation %s", r.Method)
					w.WriteHeader(500)
					return
				}
				if r.Header.Get("Authorization") != "Bearer private-test-key" {
					t.Error("missing authentication")
				}
				switch r.URL.Path {
				case "/api/system/metadata":
					_, _ = w.Write([]byte(`{"response":{"version":"3.4.3"}}`))
				case "/api/internal-squads":
					_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"internalSquads": []map[string]string{{"uuid": testSquad}}}})
				default:
					if strings.Contains(r.URL.Path, "connection-keys") && denied != 0 {
						w.WriteHeader(denied)
						_, _ = w.Write([]byte("secret-upstream-body"))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			p, err := NewRemnawave(RemnaConfig{BaseURL: srv.URL, APIToken: "private-test-key", AllowHTTP: true, Profiles: map[string][]string{"main": {testSquad}}})
			if err != nil {
				t.Fatal(err)
			}
			report := p.Check(context.Background())
			if report.Ready != (denied == 0) || report.WriteScopesVerified {
				t.Fatalf("incorrect capability report: %+v", report)
			}
			last := report.Checks[len(report.Checks)-1]
			if denied == 403 && last.Code != "missing_api_scope" {
				t.Fatalf("missing scope not diagnosed: %+v", last)
			}
			if denied == 401 && last.Code != "invalid_api_token" {
				t.Fatalf("invalid token not diagnosed: %+v", last)
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), "private-test-key") || strings.Contains(string(encoded), "secret-upstream") {
				t.Fatal("diagnostics leaked credentials")
			}
		})
	}
}
