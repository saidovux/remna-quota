package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	r.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126 Safari/537.36")
	r.Header.Set("Sec-Fetch-Dest", "document")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
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
	wantAnnounce := "base64:" + base64.StdEncoding.EncodeToString([]byte("Осталось дней: 1"))
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

func TestBrowserAlwaysGetsPageEvenWithFormat(t *testing.T) {
	h := newBrandedHandler(t, &stubService{b: testBundle(), links: []string{"vless://example"}})
	r := httptest.NewRequest(http.MethodGet, "/sub/public-token?format=json", nil)
	r.Header.Set("Accept", "text/html")
	r.Header.Set("User-Agent", "Mozilla/5.0 Chrome/126")
	r.Header.Set("Sec-Fetch-Dest", "document")
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("browser with ?format=json got %d %q", w.Code, w.Header().Get("Content-Type"))
	}
}

func TestSubscriptionAutoJSONForJSONClients(t *testing.T) {
	h := newBrandedHandler(t, &stubService{b: testBundle(), links: []string{"vless://example"}})

	incy := httptest.NewRequest(http.MethodGet, "/sub/public-token", nil)
	incy.Header.Set("User-Agent", "INCY/3.7.0/android")
	incyRec := httptest.NewRecorder()
	h.ServeHTTP(incyRec, incy)
	if !strings.HasPrefix(incyRec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("Incy should receive JSON automatically, got %q", incyRec.Header().Get("Content-Type"))
	}

	accept := httptest.NewRequest(http.MethodGet, "/sub/public-token", nil)
	accept.Header.Set("Accept", "application/json")
	acceptRec := httptest.NewRecorder()
	h.ServeHTTP(acceptRec, accept)
	if !strings.HasPrefix(acceptRec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("Accept: application/json should receive JSON, got %q", acceptRec.Header().Get("Content-Type"))
	}

	legacy := httptest.NewRequest(http.MethodGet, "/sub/public-token", nil)
	legacy.Header.Set("User-Agent", "v2rayNG/1.8")
	legacyRec := httptest.NewRecorder()
	h.ServeHTTP(legacyRec, legacy)
	if !strings.HasPrefix(legacyRec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("v2rayNG should keep the base64 list, got %q", legacyRec.Header().Get("Content-Type"))
	}

	explicit := httptest.NewRequest(http.MethodGet, "/sub/public-token?format=base64", nil)
	explicit.Header.Set("User-Agent", "INCY/3.7.0/android")
	explicitRec := httptest.NewRecorder()
	h.ServeHTTP(explicitRec, explicit)
	if !strings.HasPrefix(explicitRec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("explicit ?format=base64 must win, got %q", explicitRec.Header().Get("Content-Type"))
	}
}

func TestSubscriptionPageShowsPerChannelDaysWhenDifferent(t *testing.T) {
	h := newBrandedHandler(t, &stubService{b: testBundle()}).(*handler)
	main, cdn := 30, 5
	resp := accountResponse{
		Name: "Основной", Status: "active",
		Parts: []partResponse{
			{Key: "main", Label: "Обычный VPN", Enabled: true, Status: "active", LimitBytes: 200 << 30, UsedBytes: int64Ptr(0), ExpiresAt: time.Now().Add(30 * 24 * time.Hour), DaysLeft: &main},
			{Key: "cdn", Label: "CDN", Enabled: true, Status: "active", LimitBytes: 50 << 30, UsedBytes: int64Ptr(0), ExpiresAt: time.Now().Add(5 * 24 * time.Hour), DaysLeft: &cdn},
		},
	}
	data := h.subscriptionPageData(resp, "public-token")
	if data.DaysUniform {
		t.Fatal("differing expiries must not collapse into one number")
	}
	w := httptest.NewRecorder()
	h.renderSubscriptionPage(w, data)
	body := w.Body.String()
	for _, want := range []string{"Осталось по каналам", "Обычный VPN 30 дн.", "CDN 5 дн."} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestDaysLeftRoundsPartialDayUp(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		at   time.Time
		want int
	}{
		{"expired", now.Add(-time.Hour), 0},
		{"exact zero", time.Time{}, 0},
		{"last partial day", now.Add(3 * time.Hour), 1},
		{"almost 30 days", now.Add(29*24*time.Hour + 12*time.Hour), 30},
		{"exact 30 days", now.Add(30 * 24 * time.Hour), 30},
	}
	for _, tc := range cases {
		if got := daysLeftUntil(tc.at); got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}

func TestDaysPresentationKeepsEveryChannel(t *testing.T) {
	uniform, days, text, list := daysPresentation([]partDaysEntry{{Label: "Обычный VPN", Days: 30}, {Label: "CDN", Days: 30}})
	if !uniform || days != 30 || text != "30" || list != nil {
		t.Fatalf("uniform case: uniform=%v days=%d text=%q list=%v", uniform, days, text, list)
	}
	uniform, _, text, list = daysPresentation([]partDaysEntry{{Label: "Обычный VPN", Days: 30}, {Label: "CDN", Days: 5}})
	if uniform {
		t.Fatal("differing channels must not collapse to one number")
	}
	if !strings.Contains(text, "Обычный VPN 30 дн.") || !strings.Contains(text, "CDN 5 дн.") {
		t.Fatalf("unexpected combined text %q", text)
	}
	if len(list) != 2 {
		t.Fatalf("both channels must stay, got %d", len(list))
	}
}

func TestWantsSubscriptionPage(t *testing.T) {
	cases := []struct {
		name          string
		accept        string
		userAgent     string
		hwid          string
		secFetchDest  string
		upgradeHeader string
		want          bool
	}{
		{"browser navigation", "text/html,application/xhtml+xml", "Mozilla/5.0 Firefox/128", "", "document", "1", true},
		{"legacy browser", "text/html", "Mozilla/5.0 (Windows NT 6.1)", "", "", "1", true},
		{"client wildcard", "*/*", "v2rayNG/1.8", "", "", "", false},
		{"explicit json", "application/json", "Mozilla/5.0", "", "document", "1", false},
		{"client with hwid", "text/html", "Mozilla/5.0", "device-1", "document", "1", false},
		{"client html webview", "text/html,application/xhtml+xml", "INCY/3.7.0/android", "", "", "", false},
		{"html but not a navigation", "text/html", "UnknownBot/1.0", "", "", "", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodGet, "/sub/x", nil)
		r.Header.Set("Accept", tc.accept)
		r.Header.Set("User-Agent", tc.userAgent)
		if tc.hwid != "" {
			r.Header.Set("x-hwid", tc.hwid)
		}
		if tc.secFetchDest != "" {
			r.Header.Set("Sec-Fetch-Dest", tc.secFetchDest)
		}
		if tc.upgradeHeader != "" {
			r.Header.Set("Upgrade-Insecure-Requests", tc.upgradeHeader)
		}
		if got := wantsSubscriptionPage(r); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
