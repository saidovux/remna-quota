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

func TestDiagnosticsAuthenticatedLocalAndSecretFree(t *testing.T) {
	repo := bundle.NewMemoryRepository()
	s := bundle.NewService(repo, map[string]bundle.Provider{"demo": providers.NewDemo()})
	defer s.Close()
	b, err := s.Put(context.Background(), "secret-account-name", bundle.PutRequest{Username: "private_user", Enabled: true, Parts: []bundle.PartRequest{{Key: "main", Provider: "demo", Profile: "main", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(s, repo, Config{APIKey: testAPIKey, PublicURL: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/diagnostics", "/api/v1/metrics"} {
		if w := makeRequest(h, "GET", path, "", ""); w.Code != 401 {
			t.Fatal("public diagnostics")
		}
		if w := makeRequest(h, "POST", path, "", testAPIKey); w.Code != 405 {
			t.Fatal("invalid method accepted")
		}
		w := makeRequest(h, "GET", path, "", testAPIKey)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, secret := range []string{testAPIKey, b.ID, b.Username, b.Token} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("diagnostics leaked user data")
			}
		}
		if path == "/api/v1/diagnostics" {
			var d bundle.Diagnostics
			if json.Unmarshal(w.Body.Bytes(), &d) != nil || d.Accounts != 1 || d.PendingAccounts != 0 || d.ProviderOperations["read"].Calls != 0 || d.ProviderOperations["ensure"].Calls != 1 {
				t.Fatal("unexpected diagnostics or panel refresh")
			}
		}
	}
}

func TestRevisionAndAccountingFieldsInAPI(t *testing.T) {
	b := testBundle()
	b.Revision = 3
	now := time.Now().UTC()
	b.NextRetryAt, b.LastAttemptAt, b.FailureCount = &now, &now, 2
	b.Parts[0].AppliedRevision, b.Parts[0].AccountingStatus, b.Parts[0].ObservedAt = 2, "anomaly", &now
	h := newTestHandler(t, &stubService{b: b})
	w := makeRequest(h, "POST", "/api/v1/accounts/account-1/sync", "", testAPIKey)
	var got accountResponse
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || w.Code != 202 || got.SyncStatus != "pending" || got.Parts[0].AppliedRevision != 2 || got.Parts[0].ObservedAt == nil || got.Parts[0].AccountingStatus != "anomaly" || got.NextRetryAt == nil || got.FailureCount != 2 {
		t.Fatal("unconfirmed revision presented as applied", w.Code, w.Body.String())
	}
}
