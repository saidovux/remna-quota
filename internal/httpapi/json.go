package httpapi

import (
	"math"
	"net/http"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

type subscriptionPartJSON struct {
	Key            string     `json:"key"`
	Label          string     `json:"label"`
	Status         string     `json:"status"`
	Unlimited      bool       `json:"unlimited"`
	UsedBytes      int64      `json:"used_bytes"`
	LimitBytes     int64      `json:"limit_bytes"`
	RemainingBytes *int64     `json:"remaining_bytes,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	DaysLeft       *int       `json:"days_left,omitempty"`
}

// writeSubscriptionJSON emits a structured document with per-part state plus a
// combined view. It is intended for dashboards, bots and the website. It does
// NOT build a single importable Xray config (that is the balancer stage).
func (h *handler) writeSubscriptionJSON(w http.ResponseWriter, b bundle.Bundle, links []string) {
	parts := make([]subscriptionPartJSON, 0, len(b.Parts))
	var usedTotal, limitTotal int64
	unlimited := false
	var latest time.Time
	for _, part := range b.Parts {
		if !part.Enabled || (!part.ExpiresAt.IsZero() && !part.ExpiresAt.After(time.Now())) {
			continue
		}
		item := subscriptionPartJSON{Key: part.Key, Label: part.Label, Status: "active", UsedBytes: bundle.UsedBytes(part), LimitBytes: part.LimitBytes}
		if !part.ExpiresAt.IsZero() {
			expires := part.ExpiresAt
			item.ExpiresAt = &expires
			days := int(time.Until(expires).Hours() / 24)
			if days < 0 {
				days = 0
			}
			item.DaysLeft = &days
			if expires.After(latest) {
				latest = expires
			}
		}
		if part.LimitBytes == 0 {
			item.Unlimited = true
			unlimited = true
		} else {
			remaining := part.LimitBytes - item.UsedBytes
			if remaining < 0 {
				remaining = 0
			}
			item.RemainingBytes = &remaining
			if item.RemainingBytes != nil && *item.RemainingBytes == 0 {
				item.Status = "limited"
			}
			limitTotal = saturatingAdd(limitTotal, part.LimitBytes)
		}
		usedTotal = saturatingAdd(usedTotal, item.UsedBytes)
		parts = append(parts, item)
	}
	combined := map[string]any{"status": "active", "used_bytes": usedTotal, "unlimited": unlimited}
	if unlimited {
		combined["limit_bytes"] = 0
	} else {
		combined["limit_bytes"] = limitTotal
		remaining := limitTotal - usedTotal
		if remaining < 0 {
			remaining = 0
		}
		combined["remaining_bytes"] = remaining
	}
	if !latest.IsZero() {
		combined["expires_at"] = latest
		days := int(time.Until(latest).Hours() / 24)
		if days < 0 {
			days = 0
		}
		combined["days_left"] = days
	}
	if len(parts) == 0 {
		combined["status"] = "expired"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":               b.ID,
		"name":             b.Name,
		"lifecycle":        b.Lifecycle,
		"balance_minor":    b.BalanceMinor,
		"autorenew":        b.Autorenew,
		"pending_periods":  b.PendingPeriods,
		"subscription_url": h.publicURL + "/sub/" + b.Token,
		"urls":             links,
		"parts":            parts,
		"combined":         combined,
	})
}

func saturatingAdd(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}
