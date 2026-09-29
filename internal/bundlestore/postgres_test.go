package bundlestore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/bundle"
)

func TestPostgresDurabilityLocksAndRollback(t *testing.T) {
	dsn := os.Getenv("BACKEND_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BACKEND_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	p, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	id := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	id2 := id + "-other"
	defer p.pool.Exec(context.Background(), "DELETE FROM account_bundles WHERE id=ANY($1)", []string{id, id2})
	sum := sha256.Sum256([]byte(id))
	hash := hex.EncodeToString(sum[:])
	_, err = p.Update(ctx, id, func(b *bundle.Bundle) error {
		*b = bundle.Bundle{ID: id, Username: id, ExternalRef: id, Enabled: true, Token: "secret-test-token", TokenHash: hash, Revision: 1, Parts: []bundle.Part{{PartRequest: bundle.PartRequest{Key: "main", LimitBytes: 70}, Remote: &bundle.Remote{ID: "42", UsedBytes: 12}, Traffic: &bundle.TrafficState{UsedBytes: 42, ObservedTotalBytes: 42, RemoteID: "42", RemoteUsedBytes: 12, CounterResets: 1, AppliedLimitBytes: 70}}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	b, err := p2.ByTokenHash(ctx, hash)
	if err != nil || b.Token != "secret-test-token" || b.Parts[0].Remote.UsedBytes != 12 {
		t.Fatalf("durability: %+v %v", b, err)
	}
	if b.Parts[0].Traffic == nil || b.Parts[0].Traffic.UsedBytes != 42 || b.Parts[0].Traffic.CounterResets != 1 {
		t.Fatal("traffic ledger lost across repositories")
	}
	on := true
	page, err := p2.List(ctx, bundle.ListOptions{Limit: 1, ExternalRef: id, Search: "integration-", Enabled: &on})
	if err != nil || len(page) != 1 || page[0].ID != id {
		t.Fatalf("directory lookup: %v", err)
	}
	page, err = p2.List(ctx, bundle.ListOptions{Limit: 1, ExternalRef: id, After: id})
	if err != nil || len(page) != 0 {
		t.Fatal("directory cursor repeated row")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			repo := p
			if i%2 == 0 {
				repo = p2
			}
			_, err := repo.Update(ctx, id, func(b *bundle.Bundle) error { b.Revision++; return nil })
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err = p.Get(ctx, id)
	if err != nil || b.Revision != 21 {
		t.Fatalf("lost concurrent update: %+v %v", b, err)
	}
	wantErr := errors.New("abort")
	_, err = p.Update(ctx, id, func(b *bundle.Bundle) error { b.Revision = 999; return wantErr })
	if !errors.Is(err, wantErr) {
		t.Fatal(err)
	}
	b, _ = p.Get(ctx, id)
	if b.Revision != 21 {
		t.Fatal("rollback failed")
	}
	_, err = p.Update(ctx, id2, func(other *bundle.Bundle) error {
		*other = b
		other.ID = id2
		other.TokenHash = fmt.Sprintf("%064d", 42)
		return nil
	})
	if !errors.Is(err, bundle.ErrConflict) {
		t.Fatalf("duplicate username accepted: %v", err)
	}
	_, err = p.Get(ctx, id2)
	if !errors.Is(err, bundle.ErrNotFound) {
		t.Fatalf("conflicted row persisted: %v", err)
	}
	_, err = p.Update(ctx, id, func(b *bundle.Bundle) error { b.Token = "rotated"; b.TokenHash = fmt.Sprintf("%064d", 43); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.ByTokenHash(ctx, hash); !errors.Is(err, bundle.ErrNotFound) {
		t.Fatalf("old token still valid: %v", err)
	}
}
