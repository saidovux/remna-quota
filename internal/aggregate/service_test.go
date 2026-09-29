package aggregate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixtureProvider struct {
	mu    sync.Mutex
	rows  map[string]Snapshot
	fail  string
	reads int
}

func fixture() (*Service, *fixtureProvider) {
	expiry := time.Now().UTC().Add(time.Hour)
	p := &fixtureProvider{rows: map[string]Snapshot{"main": {ID: "main", Username: "external_main", Status: "ACTIVE", ExpiresAt: expiry, UsedBytes: 200}, "cdn": {ID: "cdn", Username: "external_cdn", Status: "ACTIVE", ExpiresAt: expiry, LimitBytes: 1000, UsedBytes: 200}}}
	return NewService(NewMemoryRepository(), map[string]Provider{"panel": p}), p
}
func (p *fixtureProvider) Read(ctx context.Context, id string) (Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	if p.fail == id {
		return Snapshot{}, ErrUnavailable
	}
	r, ok := p.rows[id]
	if !ok {
		return Snapshot{}, ErrNotFound
	}
	return r, nil
}
func (p *fixtureProvider) Links(ctx context.Context, id string) ([]string, error) {
	return []string{"vless://" + id + "@example.invalid:443#node"}, nil
}
func request() PutRequest {
	return PutRequest{Enabled: true, ExternalRef: "owner-42", Sources: []SourceInput{{Key: "main", Label: "MAIN", Provider: "panel", Reference: "main", Enabled: true}, {Key: "cdn", Label: "CDN", Provider: "panel", Reference: "cdn", Enabled: true}}}
}

func TestReferencesFollowExternalRenewalsAndIndependentQuota(t *testing.T) {
	ctx := context.Background()
	s, p := fixture()
	b, err := s.Put(ctx, "bundle-a", request())
	if err != nil {
		t.Fatal(err)
	}
	if p.reads != 0 {
		t.Fatal("composition unexpectedly contacted provider")
	}
	again, err := s.Put(ctx, b.ID, request())
	if err != nil || again.Revision != b.Revision || again.Token != b.Token {
		t.Fatal("retry lost idempotency")
	}
	links, err := s.Links(ctx, b)
	if err != nil || len(links) != 2 {
		t.Fatal("both sources not merged")
	}
	p.mu.Lock()
	cdn := p.rows["cdn"]
	cdn.UsedBytes = 1000
	p.rows["cdn"] = cdn
	p.mu.Unlock()
	links, err = s.Links(ctx, b)
	if err != nil || len(links) != 1 || strings.Contains(links[0], "CDN") {
		t.Fatal("exhausted CDN affected MAIN")
	}
	p.mu.Lock()
	cdn.LimitBytes = 2000
	cdn.ExpiresAt = cdn.ExpiresAt.Add(time.Hour)
	p.rows["cdn"] = cdn
	p.mu.Unlock()
	links, err = s.Links(ctx, b)
	if err != nil || len(links) != 2 {
		t.Fatal("external renewal was not observed")
	}
	fresh, err := s.Get(ctx, b.ID, true)
	if err != nil || fresh.Revision != b.Revision || fresh.Sources[1].Snapshot.UsedBytes != 1000 || fresh.Sources[1].Snapshot.LimitBytes != 2000 {
		t.Fatal("observation changed configuration revision or source counters")
	}
	p.mu.Lock()
	p.fail = "cdn"
	p.mu.Unlock()
	if links, err := s.Links(ctx, b); !errors.Is(err, ErrUnavailable) || len(links) != 0 {
		t.Fatal("outage returned a misleading partial subscription")
	}
	fresh, _ = s.Get(ctx, b.ID, false)
	if fresh.Sources[1].Snapshot.UsedBytes != 1000 || fresh.Sources[1].LastError == "" {
		t.Fatal("last snapshot/error was not preserved")
	}
	p.mu.Lock()
	p.fail = ""
	p.mu.Unlock()
	if links, err := s.Links(ctx, b); err != nil || len(links) != 2 {
		t.Fatal("source did not recover")
	}
	removed, err := s.RemoveSource(ctx, b.ID, "cdn", nil)
	if err != nil {
		t.Fatal(err)
	}
	if links, err := s.Links(ctx, removed); err != nil || len(links) != 1 {
		t.Fatal("detaching CDN failed")
	}
	if len(p.rows) != 2 || p.rows["cdn"].UsedBytes != 1000 {
		t.Fatal("detaching mutated external subscriptions")
	}
}

func TestMultipleBundlesAndConcurrentComposition(t *testing.T) {
	ctx := context.Background()
	s, p := fixture()
	a, err := s.Put(ctx, "a", request())
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Put(ctx, "b", request())
	if err != nil || a.Token == b.Token {
		t.Fatal("same owner cannot have independent bundles")
	}
	rows, err := s.List(ctx, ListOptions{Limit: 1, ExternalRef: "owner-42"})
	if err != nil || len(rows) != 1 || rows[0].ID != "a" {
		t.Fatal("first page incorrect")
	}
	rows, err = s.List(ctx, ListOptions{Limit: 10, ExternalRef: "owner-42", After: "a"})
	if err != nil || len(rows) != 1 || rows[0].ID != "b" {
		t.Fatal("second page incorrect")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := s.Patch(ctx, a.ID, PatchRequest{Name: &name, ExpectedRevision: &a.Revision})
			results <- err
		}(name)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrRevision) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("optimistic concurrency lost an update")
	}
	_, err = s.Put(ctx, "empty", PutRequest{Enabled: true, Sources: []SourceInput{}})
	if err != nil {
		t.Fatal(err)
	}
	results = make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.PutSource(ctx, "empty", SourceInput{Key: fmt.Sprintf("p%d", i), Provider: "panel", Reference: fmt.Sprintf("external-%d", i), Enabled: true}, nil)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	empty, err := s.Get(ctx, "empty", false)
	if err != nil || len(empty.Sources) != 8 {
		t.Fatal("atomic source updates overwrote another source")
	}
	rotated, err := s.RotateToken(ctx, b.ID, &b.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ByToken(ctx, b.Token); !errors.Is(err, ErrNotFound) {
		t.Fatal("old token remains valid")
	}
	if _, err := s.Links(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Fatal("in-flight stale token survived rotation")
	}
	if err := s.Delete(ctx, b.ID, &b.Revision); !errors.Is(err, ErrRevision) {
		t.Fatal("stale delete accepted")
	}
	if err := s.Delete(ctx, b.ID, &rotated.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ByToken(ctx, rotated.Token); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted bundle token remains valid")
	}
	if len(p.rows) != 2 {
		t.Fatal("bundle deletion removed external users")
	}
}

func TestInvalidCompositionDoesNotReplaceStoredSources(t *testing.T) {
	s, _ := fixture()
	ctx := context.Background()
	b, _ := s.Put(ctx, "safe", request())
	bad := request()
	bad.Sources[1].Reference = "main"
	if _, err := s.Put(ctx, b.ID, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("same source duplicated in bundle")
	}
	bad = request()
	bad.Sources[0].Provider = "unconfigured"
	if _, err := s.Put(ctx, b.ID, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown provider accepted")
	}
	after, _ := s.Get(ctx, b.ID, false)
	if after.Revision != b.Revision || len(after.Sources) != 2 {
		t.Fatal("invalid input changed composition")
	}
	no := false
	disabled, err := s.Patch(ctx, b.ID, PatchRequest{Enabled: &no})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ByToken(ctx, disabled.Token); !errors.Is(err, ErrNotFound) {
		t.Fatal("disabled bundle accessible")
	}
	if _, err := s.Links(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Fatal("in-flight snapshot bypassed disable")
	}
}
