package httpapi

import (
	"context"
	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/internal/providers"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHWIDHTTPAndManagement(t *testing.T) {
	repo := bundle.NewMemoryRepository()
	service := bundle.NewService(repo, map[string]bundle.Provider{"demo": providers.NewDemo()})
	limit := 1
	b, err := service.Put(context.Background(), "hwid", bundle.PutRequest{DeviceLimit: &limit, Username: "alice", Enabled: true, Parts: []bundle.PartRequest{
		{Key: "main", Provider: "demo", Profile: "main", Enabled: true, ExpiresAt: time.Now().Add(time.Hour)},
		{Key: "cdn", Provider: "demo", Profile: "cdn", Enabled: true, LimitBytes: 50, ExpiresAt: time.Now().Add(time.Hour)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(service, repo, Config{APIKey: testAPIKey, PublicURL: "https://sub.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		hwid   string
		code   int
		header string
	}{
		{"", 403, "x-hwid-not-supported"}, {"invalid", 403, "x-hwid-not-supported"},
		{"device-1234567890", 200, "x-hwid-active"}, {"device-1234567890", 200, "x-hwid-active"},
		{"device-0987654321", 403, "x-hwid-max-devices-reached"},
	} {
		r := httptest.NewRequest("GET", "/sub/"+b.Token, nil)
		r.Header.Set("x-hwid", tc.hwid)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.code || w.Header().Get(tc.header) != "true" {
			t.Fatalf("%s: %d %s", tc.hwid, w.Code, w.Body.String())
		}
	}
	path := "/api/v1/accounts/hwid/devices"
	if w := makeRequest(h, "GET", path, "", ""); w.Code != 401 {
		t.Fatal("unprotected device registry")
	}
	if w := makeRequest(h, "GET", path, "", testAPIKey); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := makeRequest(h, "DELETE", path+"/device-1234567890", "", testAPIKey); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := makeRequest(h, "PATCH", "/api/v1/accounts/hwid", `{"device_limit":0}`, testAPIKey); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := makeRequest(h, "GET", "/sub/"+b.Token, "", ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
