package bundle

import (
	"context"
	"errors"
	"time"
)

// Diagnostics contains no user identifiers, subscription links or credentials.
// Counts are per process; queue and accounting gauges are read from storage.
type Diagnostics struct {
	Accounts             int                         `json:"accounts"`
	PendingAccounts      int                         `json:"pending_accounts"`
	PendingParts         int                         `json:"pending_parts"`
	ErrorParts           int                         `json:"error_parts"`
	AccountingAnomalies  int                         `json:"accounting_anomalies"`
	UnobservedParts      int                         `json:"unobserved_parts"`
	OldestObservedAt     *time.Time                  `json:"oldest_observed_at"`
	OldestObservationAge *float64                    `json:"oldest_observation_age_seconds"`
	RunningAccounts      int                         `json:"running_accounts"`
	SyncRuns             uint64                      `json:"sync_runs_total"`
	SyncFailures         uint64                      `json:"sync_failures_total"`
	SyncSeconds          float64                     `json:"sync_duration_seconds_sum"`
	ProviderOperations   map[string]OperationMetrics `json:"provider_operations"`
}

type OperationMetrics struct {
	Calls   uint64  `json:"calls_total"`
	Errors  uint64  `json:"errors_total"`
	Seconds float64 `json:"duration_seconds_sum"`
}

func (s *Service) trackOperation(operation string, started time.Time, err error) {
	r := s.runner
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.operations[operation]
	m.Calls++
	m.Seconds += time.Since(started).Seconds()
	if err != nil && !errors.Is(err, ErrRevision) {
		m.Errors++
	}
	r.operations[operation] = m
}

func (s *Service) Diagnostics(ctx context.Context) (Diagnostics, error) {
	d := Diagnostics{ProviderOperations: map[string]OperationMetrics{}}
	r := s.runner
	r.mu.Lock()
	d.RunningAccounts, d.SyncRuns, d.SyncFailures, d.SyncSeconds = len(r.jobs), r.runs, r.failures, r.seconds
	for k, v := range r.operations {
		d.ProviderOperations[k] = v
	}
	r.mu.Unlock()
	after := ""
	for {
		rows, err := s.repo.List(ctx, ListOptions{After: after, Limit: 100})
		if err != nil {
			return Diagnostics{}, err
		}
		for _, b := range rows {
			d.Accounts++
			pending := false
			for _, p := range b.Parts {
				if p.AppliedRevision != b.Revision || p.SyncStatus != "ready" {
					d.PendingParts++
					pending = true
				}
				if p.SyncStatus == "error" {
					d.ErrorParts++
				}
				if p.AccountingStatus == "anomaly" {
					d.AccountingAnomalies++
				}
				if p.ObservedAt == nil {
					d.UnobservedParts++
				} else if d.OldestObservedAt == nil || p.ObservedAt.Before(*d.OldestObservedAt) {
					d.OldestObservedAt = p.ObservedAt
				}
			}
			if pending {
				d.PendingAccounts++
			}
			after = b.ID
		}
		if len(rows) < 100 {
			break
		}
	}
	if d.OldestObservedAt != nil {
		age := max(0, s.now().Sub(*d.OldestObservedAt).Seconds())
		d.OldestObservationAge = &age
	}
	return d, nil
}
