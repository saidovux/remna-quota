package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/saidovux/remna-quota/internal/bundle"
)

func (h *handler) diagnostics(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	service, ok := h.service.(interface {
		Diagnostics(context.Context) (bundle.Diagnostics, error)
	})
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	d, err := service.Diagnostics(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "diagnostics_unavailable")
		return
	}
	if r.URL.Path == "/api/v1/diagnostics" {
		writeJSON(w, http.StatusOK, d)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	metric := func(name, kind string, value any) {
		fmt.Fprintf(w, "# TYPE remna_quota_%s %s\nremna_quota_%s %v\n", name, kind, name, value)
	}
	metric("accounts", "gauge", d.Accounts)
	metric("pending_accounts", "gauge", d.PendingAccounts)
	metric("pending_parts", "gauge", d.PendingParts)
	metric("error_parts", "gauge", d.ErrorParts)
	metric("accounting_anomalies", "gauge", d.AccountingAnomalies)
	metric("unobserved_parts", "gauge", d.UnobservedParts)
	metric("running_accounts", "gauge", d.RunningAccounts)
	metric("sync_runs_total", "counter", d.SyncRuns)
	metric("sync_failures_total", "counter", d.SyncFailures)
	metric("sync_duration_seconds_sum", "counter", d.SyncSeconds)
	if d.OldestObservationAge != nil {
		metric("oldest_observation_age_seconds", "gauge", *d.OldestObservationAge)
	}
	keys := make([]string, 0, len(d.ProviderOperations))
	for k := range d.ProviderOperations {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Fprint(w, "# TYPE remna_quota_provider_calls_total counter\n# TYPE remna_quota_provider_errors_total counter\n# TYPE remna_quota_provider_duration_seconds_sum counter\n")
	for _, operation := range keys {
		m := d.ProviderOperations[operation]
		fmt.Fprintf(w, "remna_quota_provider_calls_total{operation=%q} %d\nremna_quota_provider_errors_total{operation=%q} %d\nremna_quota_provider_duration_seconds_sum{operation=%q} %g\n", operation, m.Calls, operation, m.Errors, operation, m.Seconds)
	}
}
