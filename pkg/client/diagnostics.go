package client

import (
	"context"
	"net/http"
	"time"
)

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

func (c *Client) Diagnostics(ctx context.Context) (Diagnostics, error) {
	var d Diagnostics
	err := c.jsonRequest(ctx, http.MethodGet, "/api/v1/diagnostics", nil, nil, &d)
	if err == nil && d.ProviderOperations == nil {
		err = ErrInvalidResponse
	}
	return d, err
}
