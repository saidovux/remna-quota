package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newBrandedHandler(t *testing.T, s *stubService) http.Handler {
	t.Helper()
	h, err := New(s, stubHealth{}, Config{
		APIKey:           testAPIKey,
		PublicURL:        "https://subscriptions.example",
		BrandName:        "FutcinVPN",
		BrandDescription: "Тестовое описание сервиса",
		BrandAnnounce:    "Осталось дней: {days}",
		BrandHomeURL:     "https://site.example",
		SupportURL:       "https://site.example/support",
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestSubscriptionPageRendersForBrowsers(t *testing.T) {
	h := newBrandedHandler(t, &stubService{b: testBundle()})
	r := httptest.NewRequest(http.MethodGet, "/sub/public-token", nil)
	r.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if contentType := w.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("content type = %q", contentType)
	}
	body := w.Body.String()
	for _, want := range []string{"FutcinVPN", "Тестовое описание сервиса", "https://subscriptions.example/sub/public-token", "MAIN", "CDN", "Потрачено", "Осталось", "/sub-assets/qrcode.js"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page does not contain %q", want)
		}
	}
}

func TestSubscriptionConfigAdvertisesProfile(t *testing.T) {
	h := newBrandedHandler(t, &stubService{b: testBundle(), links: []string{"vless://example"}})
	w := makeRequest(h, http.MethodGet, "/sub/public-token", "", "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	wantTitle := "base64:" + base64.StdEncoding.EncodeToString([]byte("FutcinVPN"))
	if got := w.Header().Get("profile-title"); got != wantTitle {
		t.Fatalf("profile-title = %q, want %q", got, wantTitle)
	}
	if got := w.Header().Get("profile-web-page-url"); got != "https://subscriptions.example/sub/public-token" {
		t.Fatalf("profile-web-page-url = %q", got)
	}
	if got := w.Header().Get("support-url"); got != "https://site.example/support" {
		t.Fatalf("support-url = %q", got)
	}
	wantAnnounce := "base64:" + base64.StdEncoding.EncodeToString([]byte("Осталось дней: 0"))
	if got := w.Header().Get("announce"); got != wantAnnounce {
		t.Fatalf("announce = %q, want %q", got, wantAnnounce)
	}
}

func TestSubscriptionUserInfoOnlyForCappedParts(t *testing.T) {
	used := int64(5_000_000_000)
	capped := []partResponse{{Enabled: true, LimitBytes: 200 << 30, UsedBytes: &used, ExpiresAt: testBundle().Parts[0].ExpiresAt}}
	info, ok := subscriptionUserInfo(capped)
	if !ok || !strings.HasPrefix(info, "upload=0; download=5000000000; total=214748364800; expire=") {
		t.Fatalf("unexpected userinfo %q ok=%v", info, ok)
	}
	unlimited := append([]partResponse{}, capped...)
	unlimited = append(unlimited, partResponse{Enabled: true, Unlimited: true})
	if _, ok := subscriptionUserInfo(unlimited); ok {
		t.Fatal("mixed unlimited bundle must not advertise a combined total")
	}
}

func TestWantsSubscriptionPage(t *testing.T) {
	cases := []struct {
		name   string
		accept string
		hwid   string
		want   bool
	}{
		{"browser", "text/html,application/xhtml+xml", "", true},
		{"client wildcard", "*/*", "", false},
		{"explicit json", "application/json", "", false},
		{"client with hwid", "text/html", "device-1", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/sub/x", nil)
		r.Header.Set("Accept", tc.accept)
		if tc.hwid != "" {
			r.Header.Set("x-hwid", tc.hwid)
		}
		if got := wantsSubscriptionPage(r); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
