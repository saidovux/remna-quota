package bundlestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/saidovux/remna-quota/internal/bundle"
	"github.com/saidovux/remna-quota/migrations"
)

type Postgres struct{ pool, operations *pgxpool.Pool }

func Open(ctx context.Context, dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	defer db.Close()
	migrator, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files, goose.WithDisableGlobalRegistry(true), goose.WithLogger(goose.NopLogger()))
	if err != nil {
		return nil, err
	}
	// A session advisory lock also serializes migrations on simultaneous startup.
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.New("database unavailable")
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(724619582337)"); err != nil {
		return nil, errors.New("migration lock unavailable")
	}
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(724619582337)")
	if _, err := migrator.Up(ctx); err != nil {
		return nil, errors.New("database migration failed")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	cfg.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("database unavailable")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("database unavailable")
	}
	opCfg := cfg.Copy()
	opCfg.MaxConns = 4
	operations, err := pgxpool.NewWithConfig(ctx, opCfg)
	if err != nil {
		pool.Close()
		return nil, errors.New("operation pool unavailable")
	}
	return &Postgres{pool: pool, operations: operations}, nil
}

func (p *Postgres) Close()                         { p.operations.Close(); p.pool.Close() }
func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

func decode(row pgx.Row) (bundle.Bundle, error) {
	var b bundle.Bundle
	var data []byte
	var token, hash string
	if err := row.Scan(&data, &token, &hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return b, bundle.ErrNotFound
		}
		return b, errors.New("database read failed")
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, errors.New("invalid stored bundle")
	}
	b.Token, b.TokenHash = token, hash
	return b, nil
}

func (p *Postgres) Get(ctx context.Context, id string) (bundle.Bundle, error) {
	return decode(p.pool.QueryRow(ctx, "SELECT state, token, token_hash FROM account_bundles WHERE id=$1", id))
}

// Remove deletes an account row. It is idempotent for the lifecycle sweep:
// a missing row returns ErrNotFound so callers can treat it as already gone.
func (p *Postgres) Remove(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, "DELETE FROM account_bundles WHERE id=$1", id)
	if err != nil {
		return errors.New("database delete failed")
	}
	if tag.RowsAffected() == 0 {
		return bundle.ErrNotFound
	}
	return nil
}

func (p *Postgres) ByTokenHash(ctx context.Context, hash string) (bundle.Bundle, error) {
	return decode(p.pool.QueryRow(ctx, "SELECT state, token, token_hash FROM account_bundles WHERE token_hash=$1", hash))
}

func (p *Postgres) ListIDs(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, "SELECT id FROM account_bundles ORDER BY id")
	if err != nil {
		return nil, errors.New("database read failed")
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, errors.New("database read failed")
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (p *Postgres) Update(ctx context.Context, id string, fn func(*bundle.Bundle) error) (bundle.Bundle, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return bundle.Bundle{}, errors.New("database transaction failed")
	}
	defer tx.Rollback(context.Background())
	// Locks absent rows too, unlike SELECT FOR UPDATE alone. Hash collisions only
	// serialize unrelated accounts, they cannot mix their state.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 724619582338))", id); err != nil {
		return bundle.Bundle{}, errors.New("account lock unavailable")
	}
	b, err := decode(tx.QueryRow(ctx, "SELECT state, token, token_hash FROM account_bundles WHERE id=$1 FOR UPDATE", id))
	if err != nil && !errors.Is(err, bundle.ErrNotFound) {
		return bundle.Bundle{}, err
	}
	if err := fn(&b); err != nil {
		return bundle.Bundle{}, err
	}
	if b.ID != id || b.Token == "" || b.TokenHash == "" {
		return bundle.Bundle{}, bundle.ErrInvalid
	}
	data, err := json.Marshal(b)
	if err != nil {
		return bundle.Bundle{}, fmt.Errorf("encode account: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO account_bundles (id,username,token,token_hash,state,external_ref,next_sync_at) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (id) DO UPDATE SET username=EXCLUDED.username, token=EXCLUDED.token, token_hash=EXCLUDED.token_hash, state=EXCLUDED.state, external_ref=EXCLUDED.external_ref, next_sync_at=EXCLUDED.next_sync_at, updated_at=now()`, id, b.Username, b.Token, b.TokenHash, data, b.ExternalRef, b.NextSyncAt)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return bundle.Bundle{}, bundle.ErrConflict
		}
		return bundle.Bundle{}, errors.New("database write failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return bundle.Bundle{}, errors.New("database commit failed")
	}
	return b, nil
}

func (p *Postgres) List(ctx context.Context, opts bundle.ListOptions) ([]bundle.Bundle, error) {
	rows, err := p.pool.Query(ctx, `SELECT state,token,token_hash FROM account_bundles
WHERE id COLLATE "C" > $1 AND ($2='' OR external_ref=$2)
AND starts_with(username,$3) AND ($4::boolean IS NULL OR (state->>'enabled')::boolean=$4)
ORDER BY id COLLATE "C" LIMIT $5`, opts.After, opts.ExternalRef, opts.Search, opts.Enabled, opts.Limit)
	if err != nil {
		return nil, errors.New("database read failed")
	}
	defer rows.Close()
	items := []bundle.Bundle{}
	for rows.Next() {
		b, err := decode(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	if rows.Err() != nil {
		return nil, errors.New("database read failed")
	}
	return items, nil
}

// This lock spans remote calls, but never holds a SQL transaction or an API
// pool connection. Failed unlocks destroy the connection rather than reuse it.
func (p *Postgres) AcquireOperation(ctx context.Context, id string) (func(), error) {
	conn, err := p.operations.Acquire(ctx)
	if err != nil {
		return nil, errors.New("operation connection unavailable")
	}
	var locked bool
	err = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,724619582340))", id).Scan(&locked)
	if err != nil {
		dead := conn.Hijack()
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = dead.Close(cleanup)
		return nil, errors.New("operation lock unavailable")
	}
	if !locked {
		conn.Release()
		return nil, bundle.ErrBusy
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var released bool
		if err := conn.QueryRow(cleanup, "SELECT pg_advisory_unlock(hashtextextended($1,724619582340))", id).Scan(&released); err != nil || !released {
			dead := conn.Hijack()
			_ = dead.Close(cleanup)
		} else {
			conn.Release()
		}
	}, nil
}

func (p *Postgres) ListDue(ctx context.Context, now time.Time, after string, limit int) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM account_bundles WHERE next_sync_at<=$1 AND id COLLATE "C">$2 ORDER BY id COLLATE "C" LIMIT $3`, now, after, limit)
	if err != nil {
		return nil, errors.New("schedule read failed")
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, errors.New("schedule read failed")
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
