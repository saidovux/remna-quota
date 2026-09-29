package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/saidovux/remna-quota/internal/config"
	"github.com/saidovux/remna-quota/internal/quota"
)

func TestStoreIntegration(t *testing.T) {
	if os.Getenv("REMNA_QUOTA_INTEGRATION_TEST") != "1" {
		t.Skip("set REMNA_QUOTA_INTEGRATION_TEST=1")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := Open(ctx, cfg.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	externalID := fmt.Sprintf("synthetic-%d", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Second)
	id, err := db.UpsertSubscription(ctx, Subscription{BedolagaSubscriptionID: externalID, TariffKey: "standard", StartAt: now, EndAt: now.Add(30 * 24 * time.Hour), Status: "active", LastSyncedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(context.Background(), "DELETE FROM subscriptions WHERE id=$1", id)
	periodID, err := db.EnsurePeriod(ctx, id, 0, now, now.Add(30*24*time.Hour), []PoolSeed{
		{Key: "main", LimitBytes: 100, AccountingSquadUUID: "11111111-1111-4111-8111-111111111111", InitialDecision: quota.Present},
		{Key: "cdn", LimitBytes: 50, AccountingSquadUUID: "22222222-2222-4222-8222-222222222222", InitialDecision: quota.Present},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := db.PoolState(ctx, periodID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsage(ctx, state.ID, 90, string(quota.Warning), now); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordUsage(ctx, state.ID, 20, string(quota.Warning), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	state, err = db.PoolState(ctx, periodID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if state.UsedBytesHighWater != 90 {
		t.Fatalf("high-water=%d", state.UsedBytesHighWater)
	}
	if state.State != quota.Warning {
		t.Fatalf("state=%s", state.State)
	}

	periodID, err = db.EnsurePeriod(ctx, id, 0, now, now.Add(30*24*time.Hour), []PoolSeed{{Key: "main", LimitBytes: 100, AccountingSquadUUID: "11111111-1111-4111-8111-111111111111", InitialDecision: quota.Present}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.PoolState(ctx, periodID, "cdn"); err == nil {
		t.Fatal("obsolete cdn pool state was not removed")
	}
	state, err = db.PoolState(ctx, periodID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if state.UsedBytesHighWater != 90 {
		t.Fatalf("retained main high-water=%d", state.UsedBytesHighWater)
	}

	lock1, acquired, err := db.TryLeaderLock(ctx)
	if err != nil || !acquired {
		t.Fatalf("first lock acquired=%v err=%v", acquired, err)
	}
	_, acquired2, err := db.TryLeaderLock(ctx)
	if err != nil || acquired2 {
		t.Fatalf("second lock acquired=%v err=%v", acquired2, err)
	}
	if err := lock1.Release(ctx); err != nil {
		t.Fatal(err)
	}
}
