package bundle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProvider models independent remote users and never resets counters.
type fakeProvider struct {
	mu            sync.Mutex
	users         map[string]Remote
	ensureFailure map[string]bool
	readFailure   map[string]bool
	linksFailure  map[string]bool
	created       int
	ensures       int
	reads         int
	linkReads     int
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{users: map[string]Remote{}, ensureFailure: map[string]bool{}, readFailure: map[string]bool{}, linksFailure: map[string]bool{}}
}

func (p *fakeProvider) Ensure(_ context.Context, owner string, part Part) (Remote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensures++
	if p.ensureFailure[part.Key] {
		return Remote{}, errors.New("upstream contains secret which must never escape")
	}
	id := owner + "/" + part.Key
	r, found := p.users[id]
	if !found {
		p.created++
		r = Remote{ID: id, Username: part.Username}
	}
	var err error
	part, err = EnforcementPart(part, r)
	if err != nil {
		return Remote{}, err
	}
	r.LimitBytes, r.ExpiresAt = part.LimitBytes, part.ExpiresAt
	r.Status = "ACTIVE"
	if !part.Enabled {
		r.Status = "DISABLED"
	} else if r.LimitBytes > 0 && r.UsedBytes >= r.LimitBytes {
		r.Status = "LIMITED"
	}
	p.users[id] = r
	return r, nil
}

func (p *fakeProvider) Read(_ context.Context, id string) (Remote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	if p.readFailure[id] {
		return Remote{}, errors.New("read unavailable")
	}
	r, ok := p.users[id]
	if !ok {
		return Remote{}, ErrNotFound
	}
	return r, nil
}

func (p *fakeProvider) Links(_ context.Context, id string) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.linkReads++
	if p.linksFailure[id] {
		return nil, errors.New("links unavailable")
	}
	r, ok := p.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	return []string{"vless://" + r.Username + "@vpn.example:443?security=tls#Node"}, nil
}

func (p *fakeProvider) SyncDevices(_ context.Context, _ string, _ Part, _ int, _ []Device) error {
	return nil
}

func (p *fakeProvider) ConfigJSON(_ context.Context, id string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.users[id]; !ok {
		return nil, ErrNotFound
	}
	return []byte(`[{"outbounds":[{"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"` + id + `"}]}}]}]`), nil
}

func setupService(t *testing.T) (*Service, *fakeProvider, PutRequest) {
	t.Helper()
	p := newFakeProvider()
	s := NewService(NewMemoryRepository(), map[string]Provider{"vpn": p})
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	return s, p, PutRequest{Username: "alice", Enabled: true, Parts: []PartRequest{
		{Key: "main", Label: "Основная", Provider: "vpn", Profile: "main", Enabled: true, ExpiresAt: now.Add(30 * 24 * time.Hour)},
		{Key: "cdn", Label: "CDN", Provider: "vpn", Profile: "cdn", Enabled: true, LimitBytes: 1000, ExpiresAt: now.Add(30 * 24 * time.Hour)},
	}}
}

func mustPut(t *testing.T, s *Service, req PutRequest) Bundle {
	t.Helper()
	b, err := s.Put(context.Background(), "account-1", req)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPutIdempotentPreservesRemoteUsageAndToken(t *testing.T) {
	s, p, req := setupService(t)
	first := mustPut(t, s, req)
	r := p.users["account-1/cdn"]
	r.UsedBytes = 700
	p.users[r.ID] = r
	second := mustPut(t, s, req)
	if first.Token != second.Token || first.TokenHash != second.TokenHash || second.Revision != 1 || p.created != 2 {
		t.Fatalf("retry changed identity: first=%+v second=%+v created=%d", first, second, p.created)
	}
	if second.Parts[0].Username != "alice_main" || second.Parts[1].Username != "alice_cdn" || second.Parts[1].Remote.UsedBytes != 700 {
		t.Fatalf("unexpected independent users: %+v", second.Parts)
	}
	req.Parts[1].LimitBytes = 2000
	third := mustPut(t, s, req)
	if third.Revision != 2 || third.Parts[1].Remote.UsedBytes != 700 || third.Parts[1].Remote.LimitBytes != 2000 {
		t.Fatalf("quota update reset usage or revision: %+v", third)
	}
}

func TestWebsiteUsernameWithPartSuffixFitsPanelContract(t *testing.T) {
	s, p, req := setupService(t)
	req.Username = "fvpn_" + strings.Repeat("a", 24)
	b := mustPut(t, s, req)
	if len(b.Parts[0].Username) != 34 || b.Parts[0].Username != req.Username+"_main" {
		t.Fatal("site-generated username not preserved")
	}
	req.Username = strings.Repeat("b", 32)
	before := p.ensures
	if _, err := s.Put(context.Background(), "another-account", req); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overlong derived username accepted: %v", err)
	}
	if p.ensures != before {
		t.Fatal("invalid username reached provider")
	}
}

func TestPartialFailureIsDurableAndRetryConverges(t *testing.T) {
	s, p, req := setupService(t)
	p.ensureFailure["cdn"] = true
	b := mustPut(t, s, req)
	if b.Parts[0].SyncStatus != "ready" || b.Parts[1].SyncStatus != "error" || b.Parts[1].LastError != "provider_ensure_failed" {
		t.Fatalf("missing partial state: %+v", b.Parts)
	}
	persisted, err := s.Get(context.Background(), b.ID, false)
	if err != nil || persisted.Token != b.Token || persisted.Parts[1].LastError != "provider_ensure_failed" {
		t.Fatalf("state lost: %+v %v", persisted, err)
	}
	if links, err := s.Links(context.Background(), b); !errors.Is(err, ErrUnavailable) || len(links) != 0 {
		t.Fatalf("partial outage served partial subscription: %v %v", links, err)
	}
	p.ensureFailure["cdn"] = false
	retried := mustPut(t, s, req)
	if p.created != 2 || retried.Token != b.Token || retried.Revision != 1 || retried.Parts[1].SyncStatus != "ready" {
		t.Fatalf("retry failed: %+v", retried)
	}
}

func TestExhaustedOrExpiredCDNDoesNotRemoveMain(t *testing.T) {
	for _, mode := range []string{"limited-status", "counter-lag", "expired-desired", "disabled-part"} {
		t.Run(mode, func(t *testing.T) {
			s, p, req := setupService(t)
			if mode == "expired-desired" {
				req.Parts[1].ExpiresAt = s.now().Add(-time.Hour)
			}
			if mode == "disabled-part" {
				req.Parts[1].Enabled = false
			}
			b := mustPut(t, s, req)
			r := p.users["account-1/cdn"]
			if mode == "limited-status" {
				r.Status = "LIMITED"
			}
			if mode == "counter-lag" {
				r.UsedBytes = r.LimitBytes
			}
			p.users[r.ID] = r
			links, err := s.Links(context.Background(), b)
			if err != nil || len(links) != 1 || !strings.Contains(links[0], "alice_main") {
				t.Fatalf("main not isolated from CDN state: %v %v", links, err)
			}
			if p.linkReads != 1 {
				t.Fatalf("retrieved ineligible CDN credentials: %d", p.linkReads)
			}
		})
	}
}

func TestDisableGatesAllPartsAndPreservesUsage(t *testing.T) {
	s, p, req := setupService(t)
	old := mustPut(t, s, req)
	r := p.users["account-1/cdn"]
	r.UsedBytes = 750
	p.users[r.ID] = r
	req.Enabled = false
	b := mustPut(t, s, req)
	for _, part := range b.Parts {
		if part.Remote.Status != "DISABLED" {
			t.Fatalf("bundle disable missed part: %+v", part)
		}
	}
	if !b.Parts[0].Enabled || !b.Parts[1].Enabled {
		t.Fatal("effective disable overwrote desired part flags")
	}
	if _, err := s.ByToken(context.Background(), old.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled token accepted: %v", err)
	}
	if _, err := s.Links(context.Background(), old); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale enabled snapshot accepted: %v", err)
	}
	req.Enabled = true
	b = mustPut(t, s, req)
	if b.Parts[1].Remote.UsedBytes != 750 || b.Parts[1].Remote.Status != "ACTIVE" {
		t.Fatalf("reactivation reset quota: %+v", b.Parts[1])
	}
}

func TestRefreshReadOnlyPreservesFailedIntentAndRecoversTransientRead(t *testing.T) {
	s, p, req := setupService(t)
	b := mustPut(t, s, req)
	ensures := p.ensures
	p.readFailure["account-1/cdn"] = true
	b, err := s.Get(context.Background(), b.ID, true)
	if err != nil || b.Parts[1].SyncStatus != "error" || b.Parts[1].Remote == nil || p.ensures != ensures {
		t.Fatalf("refresh did not retain stale state safely: %+v %v", b, err)
	}
	p.readFailure["account-1/cdn"] = false
	b, err = s.Get(context.Background(), b.ID, true)
	if err != nil || b.Parts[1].SyncStatus != "ready" {
		t.Fatalf("read recovery failed: %+v %v", b, err)
	}
	p.ensureFailure["cdn"] = true
	req.Parts[1].LimitBytes = 2000
	b = mustPut(t, s, req)
	ensures = p.ensures
	b, err = s.Get(context.Background(), b.ID, true)
	if err != nil || b.Parts[1].SyncStatus != "error" || b.Parts[1].LastError != "provider_ensure_failed" || p.ensures != ensures {
		t.Fatalf("read incorrectly confirmed failed intent: %+v %v", b, err)
	}
}

func TestOutagesAndDriftFailWholeSubscription(t *testing.T) {
	for _, failure := range []string{"read", "links", "quota-drift", "expiry-drift", "unknown-status"} {
		t.Run(failure, func(t *testing.T) {
			s, p, req := setupService(t)
			b := mustPut(t, s, req)
			r := p.users["account-1/cdn"]
			switch failure {
			case "read":
				p.readFailure[r.ID] = true
			case "links":
				p.linksFailure[r.ID] = true
			case "quota-drift":
				r.LimitBytes = 0
			case "expiry-drift":
				r.ExpiresAt = r.ExpiresAt.Add(time.Hour)
			case "unknown-status":
				r.Status = "BROKEN"
			}
			p.users[r.ID] = r
			links, err := s.Links(context.Background(), b)
			if !errors.Is(err, ErrUnavailable) || len(links) != 0 {
				t.Fatalf("outage/drift produced partial credentials: %v %v", links, err)
			}
		})
	}
}

func TestTokenRotationRevokesOldTokenAndSnapshot(t *testing.T) {
	s, _, req := setupService(t)
	b := mustPut(t, s, req)
	rotated, err := s.RotateToken(context.Background(), b.ID)
	if err != nil || rotated.Token == b.Token || rotated.Revision != 2 {
		t.Fatalf("rotation failed: %+v %v", rotated, err)
	}
	if _, err := s.ByToken(context.Background(), b.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token accepted: %v", err)
	}
	if _, err := s.ByToken(context.Background(), rotated.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Links(context.Background(), b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old request snapshot accepted after rotation: %v", err)
	}
	for _, token := range []string{"", "x", rotated.Token + "=", strings.Repeat("a", 44)} {
		if _, err := s.ByToken(context.Background(), token); !errors.Is(err, ErrNotFound) {
			t.Errorf("invalid token %q: %v", token, err)
		}
	}
}

func TestImmutableIdentityAndShapeRejectBeforeProviderMutation(t *testing.T) {
	for _, change := range []string{"username", "remove", "profile", "provider", "duplicate", "negative", "missing-expiry", "unsafe-name"} {
		t.Run(change, func(t *testing.T) {
			s, p, req := setupService(t)
			mustPut(t, s, req)
			before := p.ensures
			switch change {
			case "username":
				req.Username = "bob"
			case "remove":
				req.Parts = req.Parts[:1]
			case "profile":
				req.Parts[0].Profile = "other"
			case "provider":
				s.providers["other"] = p
				req.Parts[0].Provider = "other"
			case "duplicate":
				req.Parts[1].Key = "main"
			case "negative":
				req.Parts[1].LimitBytes = -1
			case "missing-expiry":
				req.Parts[1].ExpiresAt = time.Time{}
			case "unsafe-name":
				req.Username = "alice\nother"
			}
			if _, err := s.Put(context.Background(), "account-1", req); err == nil {
				t.Fatal("unsafe mutation accepted")
			}
			if p.ensures != before {
				t.Fatal("invalid request mutated upstream")
			}
		})
	}
}

func TestAppendingPartAndLabelChangesPreserveExistingUsers(t *testing.T) {
	s, p, req := setupService(t)
	b := mustPut(t, s, req)
	req.Parts[0].Label = "Unlimited"
	req.Parts = append(req.Parts, PartRequest{Key: "extra", Provider: "vpn", Profile: "extra", Enabled: true, ExpiresAt: req.Parts[0].ExpiresAt})
	updated := mustPut(t, s, req)
	if updated.Revision != 2 || len(updated.Parts) != 3 || p.created != 3 || b.Parts[0].Remote.ID != updated.Parts[0].Remote.ID {
		t.Fatalf("append changed existing identity: %+v", updated)
	}
}

func TestConcurrentIdenticalPutCreatesExactlyOnePair(t *testing.T) {
	s, p, req := setupService(t)
	var wg sync.WaitGroup
	failures := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := s.Put(context.Background(), "account-1", req)
			if err != nil {
				failures <- err
				return
			}
			if b.Revision != 1 {
				failures <- fmt.Errorf("revision=%d", b.Revision)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if p.created != 2 {
		t.Fatalf("concurrent retries created %d remote users", p.created)
	}
}

func TestReconcileContinuesAcrossPartialFailures(t *testing.T) {
	s, p, req := setupService(t)
	mustPut(t, s, req)
	req.Username = "bravo"
	if _, err := s.Put(context.Background(), "account-2", req); err != nil {
		t.Fatal(err)
	}
	p.ensureFailure["cdn"] = true
	before := p.ensures
	if err := s.Reconcile(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed reconciliation reported success: %v", err)
	}
	if p.ensures != before+4 {
		t.Fatal("reconciliation abandoned later accounts")
	}
	p.ensureFailure["cdn"] = false
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentPartUpdatesPreserveBothConfigurations(t *testing.T) {
	s, _, req := setupService(t)
	b := mustPut(t, s, req)
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for i, part := range req.Parts {
		part.LimitBytes = int64((i + 1) * 2000)
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.PutPart(context.Background(), b.ID, part, nil); failures <- err }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.Get(context.Background(), b.ID, false)
	if err != nil || b.Parts[0].LimitBytes != 2000 || b.Parts[1].LimitBytes != 4000 || b.Revision != 3 {
		t.Fatal("concurrent independent changes were lost")
	}
	before := b.UpdatedAt
	b, err = s.Get(context.Background(), b.ID, true)
	if err != nil || b.Revision != 3 || !b.UpdatedAt.Equal(before) {
		t.Fatal("traffic refresh changed configuration revision")
	}
}
