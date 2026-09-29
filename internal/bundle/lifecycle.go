package bundle

import "time"

// Lifecycle values stored on Bundle.Lifecycle.
const (
	LifecycleActive  = "active"
	LifecycleGrace   = "grace"
	LifecycleDeleted = "deleted"
)

// GracePeriodDays is how long a fully ended account is kept before deletion.
const GracePeriodDays = 14

// Decision is the action the lifecycle engine took for an account.
type Decision int

const (
	DecisionNone Decision = iota
	DecisionActivated
	DecisionGrace
	DecisionDelete
)

// PartEnded reports whether a part has ended: expired by date or exhausted by
// accounted traffic. A part with no expiration is treated as ended.
func PartEnded(p Part, now time.Time) bool {
	if p.ExpiresAt.IsZero() || !p.ExpiresAt.After(now) {
		return true
	}
	if p.LimitBytes > 0 && UsedBytes(p) >= p.LimitBytes {
		return true
	}
	return false
}

// AccountEnded reports whether every part has ended. An account without parts
// is considered ended (nothing left to serve).
func AccountEnded(b Bundle, now time.Time) bool {
	if len(b.Parts) == 0 {
		return true
	}
	for _, p := range b.Parts {
		if !PartEnded(p, now) {
			return false
		}
	}
	return true
}

// AccountEndsAt returns the latest part expiration, never earlier than now.
func AccountEndsAt(b Bundle, now time.Time) time.Time {
	latest := now
	for _, p := range b.Parts {
		if p.ExpiresAt.After(latest) {
			latest = p.ExpiresAt
		}
	}
	return latest
}

// Evaluate advances the account lifecycle in place and returns the action taken.
// It is pure (no IO, single time source) so it is fully unit-testable.
//
// Rules:
//   - While any part is still active the account is active.
//   - When every part has ended: spend one pending period; else, if autorenew is
//     on and the balance covers the price, charge and start a period; otherwise
//     enter grace for GracePeriodDays and finally ask for deletion.
func Evaluate(b *Bundle, now time.Time) Decision {
	now = now.UTC()

	// Disabled accounts and accounts without parts are not lifecycle-managed.
	if !b.Enabled || len(b.Parts) == 0 {
		return DecisionNone
	}

	if !AccountEnded(*b, now) {
		b.Lifecycle = LifecycleActive
		b.GraceUntil = nil
		return DecisionNone
	}

	if b.PendingPeriods > 0 {
		b.PendingPeriods--
		ActivatePeriod(b, now)
		return DecisionActivated
	}

	if b.Autorenew && b.RenewalPriceMinor > 0 && b.BalanceMinor >= b.RenewalPriceMinor {
		b.BalanceMinor -= b.RenewalPriceMinor
		ActivatePeriod(b, now)
		return DecisionActivated
	}

	if b.PendingPeriods < 0 {
		b.PendingPeriods = 0
	}

	if b.GraceUntil == nil {
		until := AccountEndsAt(*b, now).AddDate(0, 0, GracePeriodDays)
		if until.Before(now) {
			until = now.AddDate(0, 0, GracePeriodDays)
		}
		b.GraceUntil = &until
		b.Lifecycle = LifecycleGrace
		return DecisionGrace
	}
	if !b.GraceUntil.After(now) {
		b.Lifecycle = LifecycleDeleted
		return DecisionDelete
	}
	b.Lifecycle = LifecycleGrace
	return DecisionNone
}

// ActivatePeriod grants exactly one renewal period: it extends every renewal
// part by RenewalPeriodDays (starting from the later of now and the current
// expiry) and adds the per-period traffic allowance. Missing parts are created.
// The revision is bumped so providers are re-synced with the new intent.
func ActivatePeriod(b *Bundle, now time.Time) {
	now = now.UTC()
	days := b.RenewalPeriodDays
	if days <= 0 {
		days = 30
	}
	for _, rp := range b.RenewalParts {
		p := partAt(b, rp.Key)
		if p == nil {
			b.Parts = append(b.Parts, Part{
				PartRequest: PartRequest{
					Key:           rp.Key,
					Label:         rp.Label,
					Provider:      rp.Provider,
					Profile:       rp.Profile,
					LimitBytes:    rp.LimitBytes,
					ExpiresAt:     now.AddDate(0, 0, days),
					Enabled:       rp.Enabled,
					ResetStrategy: rp.ResetStrategy,
				},
				SyncStatus: "pending",
			})
			continue
		}
		base := now
		if p.ExpiresAt.After(base) {
			base = p.ExpiresAt
		}
		p.ExpiresAt = base.AddDate(0, 0, days)
		if p.LimitBytes > 0 || rp.LimitBytes > 0 {
			p.LimitBytes = addBytes(p.LimitBytes, rp.LimitBytes)
		} else {
			p.LimitBytes = 0
		}
		if rp.Label != "" {
			p.Label = rp.Label
		}
		p.Enabled = true
		p.SyncStatus, p.LastError = "pending", ""
	}
	b.GraceUntil = nil
	b.Lifecycle = LifecycleActive
	b.Revision++
}
