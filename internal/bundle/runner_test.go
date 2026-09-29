package bundle

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type controlledProvider struct {
	*fakeProvider
	ensure func(context.Context, string, Part, func(Remote) error) (Remote, error)
}

func (p *controlledProvider) EnsureObserved(ctx context.Context, id string, part Part, save func(Remote) error) (Remote, error) {
	return p.ensure(ctx, id, part, save)
}

func eventually(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("background work did not converge")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCancelledRequestAndNewRevisionConvergeWithoutOldWrites(t *testing.T) {
	s, base, req := setupService(t)
	defer s.Close()
	entered, resume := make(chan struct{}), make(chan struct{})
	var calls, obsoleteWrites atomic.Int32
	p := &controlledProvider{fakeProvider: base}
	p.ensure = func(ctx context.Context, id string, part Part, save func(Remote) error) (Remote, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-resume:
			case <-ctx.Done():
				return Remote{}, ctx.Err()
			}
			r := Remote{ID: id + "/" + part.Key, Username: part.Username, Status: "ACTIVE", ExpiresAt: part.ExpiresAt, UsedBytes: 25}
			if err := save(r); err != nil {
				return Remote{}, err
			}
			obsoleteWrites.Add(1)
		}
		return base.Ensure(ctx, id, part)
	}
	s.providers["vpn"] = p
	s.runner.wait = 15 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Put(ctx, "account-1", req); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal("HTTP cancellation not returned", err)
	}
	name := "new name"
	b, err := s.Patch(context.Background(), "account-1", PatchRequest{Name: &name})
	if err != nil || b.Revision != 2 || b.Parts[0].SyncStatus != "pending" {
		t.Fatal("slow provider blocked durable intent or confirmed old revision", err)
	}
	// The observed user exists upstream; the service must preserve its charge.
	base.users["account-1/main"] = Remote{ID: "account-1/main", Username: "alice_main", UsedBytes: 25}
	close(resume)
	eventually(t, func() bool {
		b, _ = s.repo.Get(context.Background(), "account-1")
		return b.Parts[1].AppliedRevision == 2
	})
	if obsoleteWrites.Load() != 0 || UsedBytes(b.Parts[0]) != 25 || b.Name != name {
		t.Fatal("obsolete provider write or lost observation")
	}
}

func TestRetryAfterSurvivesServiceRestartAndNewIntent(t *testing.T) {
	s, base, req := setupService(t)
	defer s.Close()
	now := s.now()
	var calls atomic.Int32
	p := &controlledProvider{fakeProvider: base, ensure: func(context.Context, string, Part, func(Remote) error) (Remote, error) {
		calls.Add(1)
		return Remote{}, &RetryError{At: now.Add(time.Minute), RateLimited: true}
	}}
	s.providers["vpn"] = p
	b := mustPut(t, s, req)
	if b.NextRetryAt == nil || !b.NextRetryAt.Equal(now.Add(time.Minute)) || b.FailureCount != 1 {
		t.Fatal("retry schedule was not persisted")
	}
	s.Close()
	other := NewService(s.repo, map[string]Provider{"vpn": p})
	defer other.Close()
	other.now = func() time.Time { return now }
	name := "changed"
	before := calls.Load()
	b, err := other.Patch(context.Background(), b.ID, PatchRequest{Name: &name})
	if err != nil || calls.Load() != before || b.NextRetryAt == nil || !b.NextSyncAt.Equal(now.Add(time.Minute)) {
		t.Fatal("restart or new intent bypassed Retry-After", err)
	}
	if err := other.ReconcileDue(context.Background()); err != nil || calls.Load() != before {
		t.Fatal("due scan bypassed durable schedule", err)
	}
}

func TestScheduleDoesNotWaitForSlowAccountAndLimitsConcurrency(t *testing.T) {
	s, base, req := setupService(t)
	defer s.Close()
	s.Configure(time.Nanosecond)
	s.now = time.Now
	var active, peak, completed atomic.Int32
	slow := make(chan struct{})
	var announced atomic.Bool
	p := &controlledProvider{fakeProvider: base, ensure: func(ctx context.Context, id string, part Part, save func(Remote) error) (Remote, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		if id == "a" {
			if announced.CompareAndSwap(false, true) {
				close(slow)
			}
			<-ctx.Done()
			return Remote{}, ctx.Err()
		}
		completed.Add(1)
		return base.Ensure(ctx, id, part)
	}}
	s.providers["vpn"] = p
	// Fill storage without initiating provider work, as after process restart.
	for i := range 7 {
		id := string(rune('a' + i))
		_, err := s.repo.Update(context.Background(), id, func(b *Bundle) error {
			*b = Bundle{ID: id, Username: "user_" + id, Token: id, TokenHash: id, Enabled: true, Revision: 1,
				Parts: []Part{{PartRequest: req.Parts[0], Username: "user_" + id + "_main", SyncStatus: "pending"}}}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ScheduleDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-slow
	eventually(t, func() bool {
		if err := s.ScheduleDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		return completed.Load() >= 6
	})
	// Another scan can run while the slow account remains blocked.
	eventually(t, func() bool {
		_ = s.ScheduleDue(context.Background())
		return completed.Load() >= 12
	})
	if peak.Load() > workerCount {
		t.Fatal("unbounded provider concurrency")
	}
}

func TestCrossReplicaOperationLockAndPartialObservation(t *testing.T) {
	s, base, req := setupService(t)
	defer s.Close()
	first := mustPut(t, s, req)
	other := NewService(s.repo, map[string]Provider{"vpn": base})
	defer other.Close()
	release, err := s.repo.AcquireOperation(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := base.ensures
	if _, err := other.Sync(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if base.ensures != before {
		t.Fatal("two replicas mutated one account")
	}
	release()
	s.providers["vpn"] = &controlledProvider{fakeProvider: base, ensure: func(ctx context.Context, id string, p Part, save func(Remote) error) (Remote, error) {
		r := *p.Remote
		r.UsedBytes = 55
		if err := save(r); err != nil {
			return Remote{}, err
		}
		return Remote{}, ErrUnavailable
	}}
	b, err := s.Sync(context.Background(), first.ID)
	if err != nil || UsedBytes(b.Parts[0]) != 55 || b.Parts[0].ObservedAt == nil || b.FailureCount != 1 {
		t.Fatal("failed later write rolled back a successful observation", err)
	}
	if _, err := s.Links(context.Background(), b); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unconfirmed state served a subscription")
	}
}

func TestRetryDelayCeiling(t *testing.T) {
	for _, failures := range []int{10, 100, 1000000} {
		d := retryDelay(failures)
		if d < 4*time.Minute || d > 5*time.Minute {
			t.Fatal(fmt.Sprintf("invalid capped retry: %s", d))
		}
	}
}
