package bundle

import (
	"context"
	"fmt"
)

// renewalPartsFrom derives the recurring period composition from the desired
// parts: one period grants exactly the current composition.
func renewalPartsFrom(parts []PartRequest) []PeriodPart {
	out := make([]PeriodPart, 0, len(parts))
	for _, p := range parts {
		out = append(out, PeriodPart{
			Key:           p.Key,
			Label:         p.Label,
			Provider:      p.Provider,
			Profile:       p.Profile,
			LimitBytes:    p.LimitBytes,
			Enabled:       true,
			ResetStrategy: p.ResetStrategy,
		})
	}
	return out
}

// AddBalance changes the account balance by deltaMinor (positive top-up, negative
// charge). The balance never goes below zero; it is left untouched otherwise.
// A non-empty key makes the call idempotent: replaying the same key is a no-op.
func (s *Service) AddBalance(ctx context.Context, id string, deltaMinor int64, key string) (Bundle, error) {
	if deltaMinor == 0 {
		return s.repo.Get(ctx, id)
	}
	return s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		if key != "" {
			for _, applied := range b.AppliedKeys {
				if applied == key {
					return nil
				}
			}
		}
		if deltaMinor < 0 && b.BalanceMinor+deltaMinor < 0 {
			return fmt.Errorf("%w: insufficient balance", ErrConflict)
		}
		b.BalanceMinor = addBytes(b.BalanceMinor, deltaMinor)
		if key != "" {
			b.AppliedKeys = append(b.AppliedKeys, key)
			if len(b.AppliedKeys) > maxAppliedKeys {
				b.AppliedKeys = b.AppliedKeys[len(b.AppliedKeys)-maxAppliedKeys:]
			}
		}
		b.UpdatedAt = s.now().UTC()
		return nil
	})
}

// SetAutorenew toggles automatic monthly renewal for the account.
func (s *Service) SetAutorenew(ctx context.Context, id string, enabled bool) (Bundle, error) {
	return s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		b.Autorenew = enabled
		b.UpdatedAt = s.now().UTC()
		return nil
	})
}

// AddPeriods queues up to maxPendingPeriods prepaid periods. Queued periods are
// activated automatically the moment the current one ends. A non-empty key makes
// the call idempotent: replaying the same key never grants a second period.
func (s *Service) AddPeriods(ctx context.Context, id string, count int, key string) (Bundle, error) {
	if count < 1 || count > maxPendingPeriods {
		return Bundle{}, fmt.Errorf("%w: count must be 1..%d", ErrInvalid, maxPendingPeriods)
	}
	return s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		if key != "" {
			for _, applied := range b.AppliedKeys {
				if applied == key {
					return nil
				}
			}
		}
		if b.PendingPeriods+count > maxPendingPeriods {
			return fmt.Errorf("%w: too many pending periods", ErrConflict)
		}
		b.PendingPeriods += count
		if key != "" {
			b.AppliedKeys = append(b.AppliedKeys, key)
			if len(b.AppliedKeys) > maxAppliedKeys {
				b.AppliedKeys = b.AppliedKeys[len(b.AppliedKeys)-maxAppliedKeys:]
			}
		}
		b.UpdatedAt = s.now().UTC()
		return nil
	})
}

// maxPendingPeriods caps the prepaid queue (e.g. up to five years of monthly
// periods). It also bounds the balanced charge a single call can schedule.
const maxPendingPeriods = 60

// maxAppliedKeys bounds the idempotency ring kept per account.
const maxAppliedKeys = 64
