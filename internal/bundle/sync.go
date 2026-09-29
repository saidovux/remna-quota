package bundle

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/saidovux/remna-quota/internal/subscription"
)

func partAt(b *Bundle, key string) *Part {
	for i := range b.Parts {
		if b.Parts[i].Key == key {
			return &b.Parts[i]
		}
	}
	return nil
}
func failureCode(err error) string {
	var retry *RetryError
	switch {
	case errors.Is(err, ErrAccounting):
		return "accounting_anomaly"
	case errors.Is(err, ErrConflict):
		return "provider_conflict"
	case errors.Is(err, ErrInvalid):
		return "provider_invalid"
	case errors.Is(err, ErrProviderAccess):
		return "provider_access_denied"
	case errors.As(err, &retry) && retry.RateLimited:
		return "provider_rate_limited"
	default:
		return "provider_ensure_failed"
	}
}

// record commits one observation only. Desired configuration is never replaced
// by a provider snapshot, even if the revision changed during the request.
func (s *Service) record(ctx context.Context, id, key string, revision int64, remote Remote, applied bool) error {
	var observationErr error
	changed := false
	_, err := s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		part := partAt(b, key)
		if part == nil {
			return ErrNotFound
		}
		changed = b.Revision != revision
		if remote.Username != part.Username || remote.ID == "" {
			observationErr = ErrConflict
		} else {
			observationErr = observe(part, remote, applied && b.Revision == revision)
		}
		if observationErr != nil {
			part.SyncStatus, part.LastError = "error", failureCode(observationErr)
			return nil
		}
		now := s.now().UTC()
		part.ObservedAt, part.SyncedAt = &now, &now
		if b.Revision != revision {
			changed = true
			part.SyncStatus, part.LastError = "pending", ""
		} else if applied {
			part.AppliedRevision = revision
			part.SyncStatus, part.LastError = "ready", ""
		} else if part.LastError == "provider_read_failed" || part.LastError == "accounting_anomaly" {
			// A recovered read confirms accounting only, never an unconfirmed mutation.
			if part.AppliedRevision == revision {
				part.SyncStatus, part.LastError = "ready", ""
			} else {
				part.SyncStatus, part.LastError = "pending", ""
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		return ErrRevision
	}
	return observationErr
}
func (s *Service) recordFailure(ctx context.Context, id, key string, revision int64, err error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, storeErr := s.repo.Update(ctx, id, func(b *Bundle) error {
		if b.ID == "" {
			return ErrNotFound
		}
		p := partAt(b, key)
		if p == nil {
			return ErrNotFound
		}
		if errors.Is(err, ErrAccounting) {
			p.AccountingStatus = "anomaly"
		}
		if b.Revision == revision {
			p.SyncStatus, p.LastError = "error", failureCode(err)
		} else {
			p.SyncStatus, p.LastError = "pending", ""
		}
		return nil
	})
	return storeErr
}

func (s *Service) syncOnce(ctx context.Context, id string, dueOnly bool) (bool, error) {
	release, err := s.repo.AcquireOperation(ctx, id)
	if err != nil {
		return false, err
	}
	defer release()
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return false, err
	}
	now := s.now().UTC()
	if dueOnly && b.NextSyncAt.After(now) {
		return false, nil
	}
	if b.RetryNotBefore != nil && b.RetryNotBefore.After(now) {
		_, err := s.repo.Update(ctx, id, func(current *Bundle) error {
			if current.RetryNotBefore != nil && current.RetryNotBefore.After(now) {
				current.NextSyncAt = *current.RetryNotBefore
				current.NextRetryAt = current.RetryNotBefore
			}
			return nil
		})
		return false, err
	}
	revision := b.Revision
	if _, err = s.repo.Update(ctx, id, func(current *Bundle) error { current.LastAttemptAt = &now; return nil }); err != nil {
		return false, err
	}
	failed := false
	retryAt := time.Time{}
	throttled := map[string]error{}
	for _, part := range b.Parts {
		if ctx.Err() != nil {
			failed = true
			if err := s.recordFailure(ctx, id, part.Key, revision, ctx.Err()); err != nil {
				return false, err
			}
			break
		}
		current, err := s.repo.Get(ctx, id)
		if err != nil {
			return false, err
		}
		if current.Revision != revision {
			return true, nil
		}
		provider := s.providers[part.Provider]
		var remote Remote
		if throttleErr := throttled[part.Provider]; throttleErr != nil {
			err = throttleErr
		} else if provider == nil {
			err = ErrInvalid
		} else {
			effective := *partAt(&current, part.Key)
			effective.Enabled = current.Enabled && effective.Enabled && effective.ExpiresAt.After(s.now())
			started := time.Now()
			if observing, ok := provider.(ObservingProvider); ok {
				remote, err = observing.EnsureObserved(ctx, id, effective, func(r Remote) error { return s.record(ctx, id, part.Key, revision, r, false) })
			} else {
				remote, err = provider.Ensure(ctx, id, effective)
			}
			s.trackOperation("ensure", started, err)
			if err == nil {
				effective.Remote = &remote
				err = s.syncDevices(ctx, current, effective)
			}
			if err == nil {
				err = s.record(ctx, id, part.Key, revision, remote, true)
			}
		}
		if errors.Is(err, ErrRevision) {
			return true, nil
		}
		if err != nil {
			failed = true
			if storeErr := s.recordFailure(ctx, id, part.Key, revision, err); storeErr != nil {
				return false, storeErr
			}
			var retry *RetryError
			if errors.As(err, &retry) && retry.At.After(retryAt) {
				throttled[part.Provider] = err
				retryAt = retry.At
				if _, storeErr := s.repo.Update(ctx, id, func(current *Bundle) error {
					current.RetryNotBefore = &retryAt
					return nil
				}); storeErr != nil {
					return false, storeErr
				}
			}
		}
	}
	changed := false
	lifecycle := DecisionNone
	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancelCommit()
	_, err = s.repo.Update(commitCtx, id, func(current *Bundle) error {
		if retryAt.After(s.now()) {
			current.RetryNotBefore = &retryAt
		}
		if current.Revision != revision {
			changed = true
			current.NextSyncAt = s.now().UTC()
			return nil
		}
		now := s.now().UTC()
		current.RetryNotBefore = nil
		if !failed {
			// Advance the subscription lifecycle only from a fully synced state.
			lifecycle = Evaluate(current, now)
		}
		if failed {
			current.FailureCount++
			next := now.Add(retryDelay(current.FailureCount))
			if retryAt.After(now) {
				current.RetryNotBefore = &retryAt
				if retryAt.After(next) {
					next = retryAt
				}
			}
			current.NextSyncAt, current.NextRetryAt = next, &next
		} else {
			current.FailureCount, current.NextRetryAt = 0, nil
			current.NextSyncAt = now.Add(s.runner.interval)
		}
		if lifecycle == DecisionActivated {
			// Re-sync immediately to push the newly granted period to providers.
			current.FailureCount, current.NextRetryAt = 0, nil
			current.NextSyncAt = now
		}
		if lifecycle == DecisionDelete {
			// Past grace: stop churn; the periodic sweep removes the account.
			current.NextSyncAt = now.Add(24 * time.Hour)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if failed && !changed {
		return changed, ErrUnavailable
	}
	return changed, nil
}

func (s *Service) readPart(ctx context.Context, b Bundle, part Part) error {
	provider := s.providers[part.Provider]
	var remote Remote
	var err error
	if provider == nil || part.Remote == nil {
		err = ErrUnavailable
	} else {
		started := time.Now()
		remote, err = provider.Read(ctx, part.Remote.ID)
		s.trackOperation("read", started, err)
		if err == nil && (remote.ID != part.Remote.ID || remote.Username != part.Username) {
			err = ErrConflict
		}
	}
	if err == nil {
		return s.record(ctx, b.ID, part.Key, b.Revision, remote, false)
	}
	_, saveErr := s.repo.Update(ctx, b.ID, func(current *Bundle) error {
		p := partAt(current, part.Key)
		if p == nil {
			return ErrNotFound
		}
		if current.Revision != b.Revision {
			return nil
		}
		if p.SyncStatus == "ready" || p.LastError == "provider_read_failed" {
			p.SyncStatus, p.LastError = "error", "provider_read_failed"
		}
		return nil
	})
	if saveErr != nil {
		return saveErr
	}
	return ErrUnavailable
}
func (s *Service) refreshAccount(ctx context.Context, id string) (Bundle, error) {
	if !accountIDPattern.MatchString(id) {
		return Bundle{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	release, err := s.acquireRead(ctx, id)
	if err != nil {
		return Bundle{}, err
	}
	defer release()
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return Bundle{}, err
	}
	for _, p := range b.Parts {
		if err := s.readPart(ctx, b, p); err != nil && ctx.Err() != nil {
			return Bundle{}, ctx.Err()
		}
	}
	return s.repo.Get(ctx, id)
}

// Links persists observations outside remote calls and verifies revision/token
// again before returning. A provider failure never produces a partial list.
func (s *Service) Links(ctx context.Context, snapshot Bundle) ([]string, error) {
	return s.LinksForDevice(ctx, snapshot, Device{})
}

func (s *Service) LinksForDevice(ctx context.Context, snapshot Bundle, device Device) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	release, err := s.acquireRead(ctx, snapshot.ID)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer release()
	b, err := s.repo.Get(ctx, snapshot.ID)
	if err != nil {
		return nil, err
	}
	if !b.Enabled || b.TokenHash != snapshot.TokenHash {
		return nil, ErrNotFound
	}
	// Reject unsupported clients before retrieving any VPN credentials. Require
	// confirmed parts before reserving a slot for a newly provisioned account.
	if DeviceLimit(b) > 0 {
		if !hwidPattern.MatchString(device.HWID) {
			return nil, ErrHWIDRequired
		}
		for _, p := range b.Parts {
			if p.AppliedRevision != b.Revision {
				return nil, ErrUnavailable
			}
		}
		b, err = s.reserveDevice(ctx, b, device)
		if err != nil {
			return nil, err
		}
		for _, p := range b.Parts {
			if err := s.syncDevices(ctx, b, p); err != nil {
				_ = s.recordFailure(ctx, b.ID, p.Key, b.Revision, err)
				return nil, ErrUnavailable
			}
		}
	}
	links := []string{}
	var fetchErr error
	for _, p := range b.Parts {
		if p.AppliedRevision != b.Revision {
			fetchErr = ErrUnavailable
			continue
		}
		if !p.Enabled || !p.ExpiresAt.After(s.now()) {
			continue
		}
		if p.SyncStatus != "ready" && !(p.SyncStatus == "error" && p.LastError == "provider_read_failed") {
			fetchErr = ErrUnavailable
			continue
		}
		if err := s.readPart(ctx, b, p); err != nil {
			fetchErr = ErrUnavailable
			continue
		}
		current, err := s.repo.Get(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		if current.TokenHash != b.TokenHash || !current.Enabled {
			return nil, ErrNotFound
		}
		if current.Revision != b.Revision {
			return nil, ErrUnavailable
		}
		part := partAt(&current, p.Key)
		remote := part.Remote
		if remote == nil || part.AccountingStatus == "anomaly" {
			fetchErr = ErrUnavailable
			continue
		}
		status := strings.ToUpper(remote.Status)
		if status == "DISABLED" || status == "LIMITED" || status == "EXPIRED" {
			continue
		}
		if status != "ACTIVE" || remote.UsedBytes < 0 || remote.LimitBytes < 0 || remote.ExpiresAt.IsZero() {
			fetchErr = ErrUnavailable
			continue
		}
		if !remote.ExpiresAt.After(s.now()) || (remote.LimitBytes > 0 && remote.UsedBytes >= remote.LimitBytes) || (part.LimitBytes > 0 && UsedBytes(*part) >= part.LimitBytes) {
			continue
		}
		effective, err := EnforcementPart(*part, *remote)
		if err != nil || effective.LimitBytes != remote.LimitBytes || remote.ExpiresAt.Unix() != part.ExpiresAt.Unix() {
			_, err = s.repo.Update(ctx, b.ID, func(row *Bundle) error {
				if row.Revision != b.Revision {
					return nil
				}
				p := partAt(row, part.Key)
				p.SyncStatus, p.LastError = "error", "provider_drift"
				return nil
			})
			if err != nil {
				return nil, err
			}
			fetchErr = ErrUnavailable
			continue
		}
		started := time.Now()
		raw, err := s.providers[part.Provider].Links(ctx, remote.ID)
		s.trackOperation("links", started, err)
		if err != nil || len(raw) == 0 {
			fetchErr = ErrUnavailable
			continue
		}
		for _, link := range raw {
			labeled, err := subscription.LabelLink(link, part.Label)
			if err != nil {
				fetchErr = ErrUnavailable
				break
			}
			links = append(links, labeled)
		}
	}
	current, err := s.repo.Get(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	if !current.Enabled || current.TokenHash != b.TokenHash {
		return nil, ErrNotFound
	}
	if current.Revision != b.Revision {
		return nil, ErrUnavailable
	}
	if fetchErr != nil {
		return nil, fetchErr
	}
	return links, nil
}
