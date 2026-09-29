package bundlestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/migrations"
)

func TestLegacyUpgradeAndCrossReplicaOperationLock(t *testing.T) {
	dsn := os.Getenv("BACKEND_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BACKEND_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	schema := fmt.Sprintf("quota_upgrade_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["search_path"] = schema
	registered := stdlib.RegisterConnConfig(cfg)
	defer stdlib.UnregisterConnConfig(registered)
	db, err := sql.Open("pgx", registered)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files, goose.WithDisableGlobalRegistry(true), goose.WithLogger(goose.NopLogger()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpTo(ctx, 4); err != nil {
		t.Fatal(err)
	}
	legacy := bundle.Bundle{ID: "legacy", Username: "legacy", Revision: 7, Parts: []bundle.Part{{PartRequest: bundle.PartRequest{Key: "cdn", ResetStrategy: "NO_RESET", LimitBytes: 100}, Traffic: &bundle.TrafficState{UsedBytes: 70, RemoteID: "42", RemoteUsedBytes: 20, AppliedLimitBytes: 100, ObservedTotalBytes: 70}}}}
	data, _ := json.Marshal(legacy)
	if _, err := db.ExecContext(ctx, "INSERT INTO account_bundles(id,username,token,token_hash,state) VALUES('legacy','legacy','original-link',$1,$2)", strings.Repeat("a", 64), data); err != nil {
		t.Fatal(err)
	}
	upgradeDSN := dsn + " search_path=" + schema
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		upgradeDSN = u.String()
	}
	p, err := Open(ctx, upgradeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	p2, err := Open(ctx, upgradeDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer p2.Close()
	b, err := p2.Get(ctx, "legacy")
	if err != nil || b.Token != "original-link" || b.Revision != 7 || bundle.UsedBytes(b.Parts[0]) != 70 {
		t.Fatal("upgrade changed identity or charges", err)
	}
	ids, err := p.ListDue(ctx, time.Now(), "", 10)
	if err != nil || len(ids) != 1 || ids[0] != "legacy" {
		t.Fatal("legacy row was not scheduled", err)
	}
	release, err := p.AcquireOperation(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if otherRelease, err := p2.AcquireOperation(ctx, "legacy"); !errors.Is(err, bundle.ErrBusy) {
		if otherRelease != nil {
			otherRelease()
		}
		t.Fatal("operation not serialized across replicas", err)
	}
	fast, cancelFast := context.WithTimeout(ctx, time.Second)
	defer cancelFast()
	next := time.Now().UTC().Add(3 * time.Minute)
	if _, err := p2.Update(fast, "legacy", func(b *bundle.Bundle) error {
		b.Revision++
		b.FailureCount, b.NextSyncAt, b.NextRetryAt, b.RetryNotBefore = 4, next, &next, &next
		return nil
	}); err != nil {
		t.Fatal("operation lock held a state transaction", err)
	}
	b, err = p.Get(ctx, "legacy")
	if err != nil || b.FailureCount != 4 || b.RetryNotBefore == nil || !b.NextSyncAt.Equal(next) {
		t.Fatal("retry not durable", err)
	}
	ids, err = p.ListDue(ctx, time.Now(), "", 10)
	if err != nil || len(ids) != 0 {
		t.Fatal("retry selected before its due time", err)
	}
}
