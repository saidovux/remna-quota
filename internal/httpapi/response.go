package httpapi

import (
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

// daysLeftUntil returns the number of calendar days left, rounding a partial
// day up: a freshly issued 30-day subscription reads 30, the final partial day
// reads 1, and an expired or zero time reads 0.
func daysLeftUntil(expiresAt time.Time) int {
	if expiresAt.IsZero() {
		return 0
	}
	remaining := time.Until(expiresAt)
	if remaining <= 0 {
		return 0
	}
	return int(math.Ceil(remaining.Hours() / 24))
}

type accountResponse struct {
	DeviceLimit       int            `json:"device_limit"`
	DeviceCount       int            `json:"device_count"`
	BalanceMinor      int64          `json:"balance_minor"`
	PendingPeriods    int            `json:"pending_periods"`
	RenewalPriceMinor int64          `json:"renewal_price_minor"`
	RenewalPeriodDays int            `json:"renewal_period_days"`
	Autorenew         bool           `json:"autorenew"`
	Lifecycle         string         `json:"lifecycle,omitempty"`
	GraceUntil        *time.Time     `json:"grace_until,omitempty"`
	NextRetryAt       *time.Time     `json:"next_retry_at"`
	LastAttemptAt     *time.Time     `json:"last_attempt_at"`
	FailureCount      int            `json:"failure_count"`
	ID                string         `json:"id"`
	Name              string         `json:"name"`
	ExternalRef       string         `json:"external_ref"`
	Status            string         `json:"status"`
	ActiveParts       int            `json:"active_parts"`
	Username          string         `json:"username"`
	Enabled           bool           `json:"enabled"`
	SubscriptionURL   string         `json:"subscription_url"`
	Parts             []partResponse `json:"parts"`
	SyncStatus        string         `json:"sync_status"`
	Stale             bool           `json:"stale"`
	Revision          int64          `json:"revision"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
}

type partResponse struct {
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

func (h *handler) accountResponse(b bundle.Bundle) accountResponse {
	response := accountResponse{ID: b.ID, Username: b.Username, Enabled: b.Enabled, SubscriptionURL: h.publicURL + "/sub/" + url.PathEscape(b.Token), Parts: make([]partResponse, 0, len(b.Parts)), SyncStatus: "ready", Revision: b.Revision, CreatedAt: b.CreatedAt, UpdatedAt: b.UpdatedAt}
	response.Name, response.ExternalRef = b.Name, b.ExternalRef
	response.DeviceLimit, response.DeviceCount = bundle.DeviceLimit(b), len(b.Devices)
	response.NextRetryAt, response.LastAttemptAt, response.FailureCount = b.NextRetryAt, b.LastAttemptAt, b.FailureCount
	response.BalanceMinor, response.Autorenew, response.PendingPeriods = b.BalanceMinor, b.Autorenew, b.PendingPeriods
	response.RenewalPriceMinor, response.RenewalPeriodDays = b.RenewalPriceMinor, b.RenewalPeriodDays
	response.Lifecycle, response.GraceUntil = b.Lifecycle, b.GraceUntil
	for _, part := range b.Parts {
		item := partResponse{Key: part.Key, Label: part.Label, Provider: part.Provider, Profile: part.Profile, Username: part.Username, Enabled: part.Enabled, LimitBytes: part.LimitBytes, ExpiresAt: part.ExpiresAt, DesiredLimitBytes: part.LimitBytes, DesiredExpiresAt: part.ExpiresAt, ResetStrategy: part.ResetStrategy, Status: "pending", SyncStatus: part.SyncStatus, Stale: part.SyncStatus != "ready", LastError: part.LastError, SyncedAt: part.SyncedAt}
		item.ObservedAt, item.AppliedRevision, item.AccountingStatus = part.ObservedAt, part.AppliedRevision, part.AccountingStatus
		if item.AccountingStatus == "" {
			item.AccountingStatus = "unknown"
		}
		if item.AppliedRevision != b.Revision && item.SyncStatus == "ready" {
			item.SyncStatus, item.Stale = "pending", true
		}
		if part.Remote != nil {
			used := bundle.UsedBytes(part)
			item.UsedBytes = &used
			item.LimitBytes = part.Remote.LimitBytes
			nativeUsed, nativeLimit := part.Remote.UsedBytes, part.Remote.LimitBytes
			item.ProviderUsedBytes, item.ProviderLimitBytes = &nativeUsed, &nativeLimit
			if part.Traffic != nil {
				item.LimitBytes = part.Traffic.AppliedLimitBytes
				total := part.Traffic.ObservedTotalBytes
				item.ObservedTotalBytes = &total
				item.CounterResets = part.Traffic.CounterResets
			}
			item.ExpiresAt = part.Remote.ExpiresAt
			item.ProviderUserID = part.Remote.ID
			item.Status = strings.ToLower(part.Remote.Status)
			if item.Status == "" {
				item.Status = "unknown"
			}
		}
		item.Unlimited = item.LimitBytes == 0
		if !item.Unlimited && item.UsedBytes != nil {
			remaining := item.LimitBytes - *item.UsedBytes
			if remaining < 0 {
				remaining = 0
			}
			item.RemainingBytes = &remaining
		}
		if !item.ExpiresAt.IsZero() {
			days := daysLeftUntil(item.ExpiresAt)
			item.DaysLeft = &days
		}
		if !b.Enabled || !part.Enabled {
			item.Status = "disabled"
		} else if (!part.ExpiresAt.IsZero() && !part.ExpiresAt.After(time.Now())) || (!item.ExpiresAt.IsZero() && !item.ExpiresAt.After(time.Now())) {
			item.Status = "expired"
		} else if item.RemainingBytes != nil && *item.RemainingBytes == 0 {
			item.Status = "limited"
		}
		if item.Stale {
			response.Stale = true
			if response.SyncStatus != "error" {
				response.SyncStatus = "pending"
			}
			if part.SyncStatus == "error" {
				response.SyncStatus = "error"
			}
		}
		observed := item.ObservedAt
		if observed == nil {
			observed = item.SyncedAt
		}
		if observed == nil || time.Since(*observed) > h.staleAfter {
			item.Stale, response.Stale = true, true
		}
		response.Parts = append(response.Parts, item)
		if item.Status == "active" && !item.Stale {
			response.ActiveParts++
		}
	}
	response.Status = "unavailable"
	if response.ActiveParts > 0 {
		response.Status = "partial"
		if response.ActiveParts == len(response.Parts) {
			response.Status = "active"
		}
	}
	if !b.Enabled {
		response.Status = "disabled"
	}
	return response
}
