package bundlestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/aggregate"
	"github.com/saidovux/remna-quota/internal/providers"
)

func TestAggregationPersistenceConcurrencyAndRemoval(t *testing.T) {
	dsn := os.Getenv("BACKEND_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BACKEND_TEST_DATABASE_URL to disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	p, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p2, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	r1, r2 := p.Aggregates(), p2.Aggregates()
	provider := providers.NewDemoReferences()
	s1 := aggregate.NewService(r1, map[string]aggregate.Provider{"demo": provider})
	s2 := aggregate.NewService(r2, map[string]aggregate.Provider{"demo": provider})
	prefix := fmt.Sprintf("aggregate-%d", time.Now().UnixNano())
	id1, id2 := prefix+"-a", prefix+"-b"
	defer r1.Delete(context.Background(), id1, nil)
	defer r1.Delete(context.Background(), id2, nil)
	wanted := aggregate.PutRequest{Name: "Same display name allowed", ExternalRef: prefix, Enabled: true, Sources: []aggregate.SourceInput{{Key: "main", Provider: "demo", Reference: "main", Enabled: true}}}
	b, err := s1.Put(ctx, id1, wanted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Put(ctx, id2, wanted); err != nil {
		t.Fatal("multiple bundles per owner not persisted")
	}
	fromOther, err := s2.ByToken(ctx, b.Token)
	if err != nil || fromOther.ID != id1 {
		t.Fatal("token lookup not durable")
	}
	if _, err := s1.Get(ctx, id1, true); err != nil {
		t.Fatal(err)
	}
	persisted, err := s2.Get(ctx, id1, false)
	if err != nil || persisted.Sources[0].Snapshot == nil {
		t.Fatal("source snapshot not durable")
	}
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := s1
			if i%2 == 0 {
				s = s2
			}
			name := fmt.Sprintf("choice-%d", i)
			_, err := s.Patch(ctx, id1, aggregate.PatchRequest{Name: &name, ExpectedRevision: &b.Revision})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, aggregate.ErrRevision) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 11 {
		t.Fatal("cross-process revision check not atomic")
	}
	rows, err := s1.List(ctx, aggregate.ListOptions{Limit: 1, ExternalRef: prefix})
	if err != nil || len(rows) != 1 || rows[0].ID != id1 {
		t.Fatal("database cursor page incorrect")
	}
	rows, err = s2.List(ctx, aggregate.ListOptions{Limit: 10, ExternalRef: prefix, After: id1})
	if err != nil || len(rows) != 1 || rows[0].ID != id2 {
		t.Fatal("database next page incorrect")
	}
	current, _ := s1.Get(ctx, id1, false)
	abort := errors.New("test rollback")
	if _, err := r1.Update(ctx, id1, func(b *aggregate.Bundle) error { b.Name = "should roll back"; return abort }); !errors.Is(err, abort) {
		t.Fatal(err)
	}
	after, _ := s2.Get(ctx, id1, false)
	if after.Name != current.Name {
		t.Fatal("failed mutation committed")
	}
	rotated, err := s1.RotateToken(ctx, id1, &current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s2.ByToken(ctx, b.Token); !errors.Is(err, aggregate.ErrNotFound) {
		t.Fatal("revocation not visible to another replica")
	}
	if err := s2.Delete(ctx, id1, &current.Revision); !errors.Is(err, aggregate.ErrRevision) {
		t.Fatal("stale delete succeeded")
	}
	if err := s2.Delete(ctx, id1, &rotated.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s1.ByToken(ctx, rotated.Token); !errors.Is(err, aggregate.ErrNotFound) {
		t.Fatal("deleted token still resolvable")
	}
	if _, err := s1.Get(ctx, id2, false); err != nil {
		t.Fatal("deleting a bundle affected another")
	}
}
