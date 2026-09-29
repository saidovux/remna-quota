package bundle

import (
	"context"
	"errors"
)

// DeleteAccount permanently removes an account: first every owned remote user,
// then the local record. It is idempotent — an already removed account (or a
// missing remote user) is not an error.
func (s *Service) DeleteAccount(ctx context.Context, id string) error {
	if !accountIDPattern.MatchString(id) {
		return ErrInvalid
	}
	release, err := s.repo.AcquireOperation(ctx, id)
	if err != nil {
		return err
	}
	defer release()

	b, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}

	for _, p := range b.Parts {
		provider := s.providers[p.Provider]
		if provider == nil || p.Remote == nil || p.Remote.ID == "" {
			continue
		}
		deleter, ok := provider.(Deleter)
		if !ok {
			continue
		}
		if err := deleter.Delete(ctx, p.Remote.ID); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}

	if err := s.repo.Remove(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// SweepDeleted deletes accounts whose grace period has fully expired (their
// lifecycle was set to "deleted" by Evaluate). It is invoked periodically by
// the scheduler and is safe to run from multiple replicas (the per-account
// operation lock serializes work; ErrBusy is skipped and retried next sweep).
func (s *Service) SweepDeleted(ctx context.Context) error {
	after := ""
	for {
		rows, err := s.repo.List(ctx, ListOptions{Limit: 100, After: after})
		if err != nil {
			return err
		}
		for _, b := range rows {
			after = b.ID
			if b.Lifecycle != LifecycleDeleted {
				continue
			}
			if err := s.DeleteAccount(ctx, b.ID); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrBusy) {
				return err
			}
		}
		if len(rows) < 100 {
			return nil
		}
	}
}
