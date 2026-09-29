package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandler(t *testing.T) {
	m := NewMetrics()
	m.Inc("remna_quota_usage_checks_total")
	m.SetPoolUsageRatio("main", .5)
	rr := httptest.NewRecorder()
	m.Handler(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rr.Body.String(), "remna_quota_usage_checks_total 1") || !strings.Contains(rr.Body.String(), `pool="main"} 0.5`) {
		t.Fatal(rr.Body.String())
	}
}
