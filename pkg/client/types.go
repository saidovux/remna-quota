// Package client integrates any trusted application with remna-quota.
// Accounts create and manage independent quota parts behind one subscription.
// Bundle methods additionally aggregate existing users without provisioning.
// Keep the API key on your server.
package client

import "time"

// PutAccountRequest is the complete desired account configuration. Existing
// usernames, keys, providers and profiles cannot be replaced. Retain disabled
// parts in subsequent requests; new parts may be appended.
type PutAccountRequest struct {
	DeviceLimit       *int          `json:"device_limit,omitempty"` // Omit to preserve; zero disables HWID.
	Name              string        `json:"name"`
	ExternalRef       string        `json:"external_ref"`
	ExpectedRevision  *int64        `json:"expected_revision,omitempty"`
	Username          string        `json:"username"`
	Enabled           bool          `json:"enabled"`
	Parts             []PartRequest `json:"parts"`
	RenewalPriceMinor *int64        `json:"renewal_price_minor,omitempty"`
	RenewalPeriodDays *int          `json:"renewal_period_days,omitempty"`
}

type PartRequest struct {
	Key           string    `json:"key"`
	Label         string    `json:"label"`
	Provider      string    `json:"provider"`
	Profile       string    `json:"profile"`
	LimitBytes    int64     `json:"limit_bytes"` // Zero means unlimited.
	ExpiresAt     time.Time `json:"expires_at"`
	Enabled       bool      `json:"enabled"`
	ResetStrategy string    `json:"reset_strategy"` // NO_RESET, DAY, WEEK, MONTH, MONTH_ROLLING.
}

// Account is returned even when a provider change is pending or failed (HTTP
// 202). Check SyncStatus and Stale before displaying provisioning as complete.
type Account struct {
	DeviceLimit       int        `json:"device_limit"`
	DeviceCount       int        `json:"device_count"`
	BalanceMinor      int64      `json:"balance_minor"`
	PendingPeriods    int        `json:"pending_periods"`
	RenewalPriceMinor int64      `json:"renewal_price_minor"`
	RenewalPeriodDays int        `json:"renewal_period_days"`
	Autorenew         bool       `json:"autorenew"`
	Lifecycle         string     `json:"lifecycle,omitempty"`
	GraceUntil        *time.Time `json:"grace_until,omitempty"`
	NextRetryAt       *time.Time `json:"next_retry_at"`
	LastAttemptAt     *time.Time `json:"last_attempt_at"`
	FailureCount      int        `json:"failure_count"`
	Name              string     `json:"name"`
	ExternalRef       string     `json:"external_ref"`
	Status            string     `json:"status"`
	ActiveParts       int        `json:"active_parts"`
	ID                string     `json:"id"`
	Username          string     `json:"username"`
	Enabled           bool       `json:"enabled"`
	SubscriptionURL   string     `json:"subscription_url"`
	Parts             []Part     `json:"parts"`
	SyncStatus        string     `json:"sync_status"`
	Stale             bool       `json:"stale"`
	Revision          int64      `json:"revision"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// Part reports its own independent quota. A nil UsedBytes means usage is not
// known; nil RemainingBytes means either unlimited or unknown (see Unlimited).
// Desired fields may differ from observed fields while reconciliation is pending.
type Part struct {
	ObservedAt         *time.Time `json:"observed_at"`
	AppliedRevision    int64      `json:"applied_revision"`
	AccountingStatus   string     `json:"accounting_status"`
	Key                string     `json:"key"`
	Label              string     `json:"label"`
	Provider           string     `json:"provider"`
	Profile            string     `json:"profile"`
	Username           string     `json:"username"`
	Enabled            bool       `json:"enabled"`
	ProviderUserID     string     `json:"provider_user_id,omitempty"`
	UsedBytes          *int64     `json:"used_bytes"`
	ProviderUsedBytes  *int64     `json:"provider_used_bytes"`
	ProviderLimitBytes *int64     `json:"provider_limit_bytes"`
	ObservedTotalBytes *int64     `json:"observed_total_bytes"`
	CounterResets      int64      `json:"counter_resets"`
	LimitBytes         int64      `json:"limit_bytes"`
	Unlimited          bool       `json:"unlimited"`
	RemainingBytes     *int64     `json:"remaining_bytes"`
	DaysLeft           *int       `json:"days_left"`
	ExpiresAt          time.Time  `json:"expires_at"`
	DesiredLimitBytes  int64      `json:"desired_limit_bytes"`
	DesiredExpiresAt   time.Time  `json:"desired_expires_at"`
	ResetStrategy      string     `json:"reset_strategy"`
	Status             string     `json:"status"`
	SyncStatus         string     `json:"sync_status"`
	Stale              bool       `json:"stale"`
	LastError          string     `json:"last_error,omitempty"`
	SyncedAt           *time.Time `json:"synced_at,omitempty"`
}
