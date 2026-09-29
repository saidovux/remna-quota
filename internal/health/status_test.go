package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadyHandler(t *testing.T) {
	s := New()
	s.Set("database", true, "")
	s.Set("contracts", false, "exact_usage_unsupported")
	rr := httptest.NewRecorder()
	s.ReadyHandler(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable || !strings.Contains(rr.Body.String(), "exact_usage_unsupported") {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
}
