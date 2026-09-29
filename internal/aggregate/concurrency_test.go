package aggregate

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type delayedReader struct {
	*fixtureProvider
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *delayedReader) Read(ctx context.Context, id string) (Snapshot, error) {
	r, err := p.fixtureProvider.Read(ctx, id)
	if p.calls.Add(1) == 1 {
		close(p.started)
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-p.release:
		}
	}
	return r, err
}

func TestSlowObservationDoesNotLockWritesOrOverwriteNewerSnapshot(t *testing.T) {
	s, fixture := fixture()
	p := &delayedReader{fixtureProvider: fixture, started: make(chan struct{}), release: make(chan struct{})}
	s.providers["panel"] = p
	r := request()
	r.Sources = r.Sources[:1]
	b, err := s.Put(context.Background(), "slow", r)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Links(ctx, b); done <- err }()
	<-p.started
	fixture.mu.Lock()
	row := fixture.rows["main"]
	row.UsedBytes = 999
	fixture.rows["main"] = row
	fixture.mu.Unlock()
	updated, err := s.Get(ctx, b.ID, true)
	if err != nil || updated.Sources[0].Snapshot.UsedBytes != 999 {
		t.Fatal("refresh blocked by a remote request", err)
	}
	name := "edited during remote read"
	if _, err := s.Patch(ctx, b.ID, PatchRequest{Name: &name}); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if err := <-done; !errors.Is(err, ErrUnavailable) {
		t.Fatal("obsolete links served", err)
	}
	updated, err = s.Get(ctx, b.ID, false)
	if err != nil || updated.Name != name || updated.Sources[0].Snapshot.UsedBytes != 999 {
		t.Fatal("obsolete read overwrote current snapshot", err)
	}
}

func TestRotateDuringReadRevokesInFlightLink(t *testing.T) {
	s, fixture := fixture()
	p := &delayedReader{fixtureProvider: fixture, started: make(chan struct{}), release: make(chan struct{})}
	s.providers["panel"] = p
	r := request()
	r.Sources = r.Sources[:1]
	b, _ := s.Put(context.Background(), "rotate", r)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Links(ctx, b); done <- err }()
	<-p.started
	if _, err := s.RotateToken(ctx, b.ID, nil); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatal("rotated link still usable", err)
	}
}
