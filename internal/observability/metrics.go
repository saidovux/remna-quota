package observability

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var metricNames = []string{
	"remna_quota_bedolaga_requests_total", "remna_quota_remnawave_requests_total", "remna_quota_external_api_errors_total",
	"remna_quota_usage_checks_total", "remna_quota_usage_check_errors_total", "remna_quota_enforcement_total", "remna_quota_enforcement_errors_total",
	"remna_quota_exhausted_pools", "remna_quota_last_bedolaga_sync_timestamp", "remna_quota_last_remnawave_check_timestamp", "remna_quota_reconcile_duration_seconds",
}

type Metrics struct {
	mu         sync.RWMutex
	values     map[string]float64
	poolRatios map[string]float64
}

func NewMetrics() *Metrics {
	m := &Metrics{values: map[string]float64{}, poolRatios: map[string]float64{}}
	for _, name := range metricNames {
		m.values[name] = 0
	}
	return m
}
func (m *Metrics) Inc(name string) { m.Add(name, 1) }
func (m *Metrics) Add(name string, delta float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[name]; ok {
		m.values[name] += delta
	}
}
func (m *Metrics) Set(name string, value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[name]; ok {
		m.values[name] = value
	}
}
func (m *Metrics) SetPoolUsageRatio(pool string, value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.poolRatios[pool] = value
}
func (m *Metrics) Handler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := append([]string(nil), metricNames...)
	sort.Strings(names)
	for _, name := range names {
		_, _ = fmt.Fprintf(w, "# TYPE %s gauge\n%s %s\n", name, name, strconv.FormatFloat(m.values[name], 'g', -1, 64))
	}
	pools := make([]string, 0, len(m.poolRatios))
	for pool := range m.poolRatios {
		pools = append(pools, pool)
	}
	sort.Strings(pools)
	_, _ = io.WriteString(w, "# TYPE remna_quota_pool_usage_ratio gauge\n")
	for _, pool := range pools {
		_, _ = fmt.Fprintf(w, "remna_quota_pool_usage_ratio{pool=%q} %s\n", sanitizeLabel(pool), strconv.FormatFloat(m.poolRatios[pool], 'g', -1, 64))
	}
}
func sanitizeLabel(value string) string {
	return strings.NewReplacer("\\", "_", "\n", "_", "\r", "_", "\"", "_").Replace(value)
}
