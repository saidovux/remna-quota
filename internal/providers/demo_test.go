package providers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/saidovux/remna-quota/internal/bundle"
)

func TestDemoPartsRemainIndependentAndAreClearlyPlaceholders(t *testing.T) {
	p := NewDemo()
	ctx := context.Background()
	cdn := testPart()
	main := cdn
	main.Key = "main"
	main.Username = "alice_main"
	main.LimitBytes = 0
	a, err := p.Ensure(ctx, "owner", main)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Ensure(ctx, "owner", cdn)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("parts share remote identity")
	}
	for _, id := range []string{a.ID, b.ID} {
		links, err := p.Links(ctx, id)
		if err != nil || len(links) != 1 || !strings.Contains(links[0], "demo.invalid") || !strings.Contains(links[0], "DEMO%20ONLY") {
			t.Fatalf("invalid demo links: %v %v", links, err)
		}
	}
	cdn.Enabled = false
	if _, err := p.Ensure(ctx, "owner", cdn); err != nil {
		t.Fatal(err)
	}
	if links, err := p.Links(ctx, b.ID); err != nil || len(links) != 0 {
		t.Fatal("disabled part emits links")
	}
	if links, err := p.Links(ctx, a.ID); err != nil || len(links) != 1 {
		t.Fatal("main part changed when disabling CDN")
	}
	if _, err := p.Ensure(ctx, "another-owner", main); !errors.Is(err, bundle.ErrConflict) {
		t.Fatalf("foreign username adopted: %v", err)
	}
}

func TestDemoEnsurePreservesCounters(t *testing.T) {
	p := NewDemo()
	part := testPart()
	ctx := context.Background()
	r, err := p.Ensure(ctx, "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	r.UsedBytes = 150
	p.users[r.ID] = r
	r, err = p.Ensure(ctx, "owner", part)
	if err != nil {
		t.Fatal(err)
	}
	if r.UsedBytes != 150 || r.Status != "LIMITED" {
		t.Fatalf("counter reset: %#v", r)
	}
}
