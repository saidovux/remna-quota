package bundlestore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/saidovux/remna-quota/internal/aggregate"
)

type Aggregates struct{ pool *pgxpool.Pool }

func (p *Postgres) Aggregates() *Aggregates          { return &Aggregates{pool: p.pool} }
func (p *Aggregates) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

func decodeAggregate(row pgx.Row) (aggregate.Bundle, error) {
	var b aggregate.Bundle
	var data []byte
	var token, hash string
	if err := row.Scan(&data, &token, &hash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return b, aggregate.ErrNotFound
		}
		return b, errors.New("bundle read failed")
	}
	if json.Unmarshal(data, &b) != nil {
		return b, errors.New("invalid stored bundle")
	}
	b.Token, b.TokenHash = token, hash
	return b, nil
}

func (p *Aggregates) Get(ctx context.Context, id string) (aggregate.Bundle, error) {
	return decodeAggregate(p.pool.QueryRow(ctx, "SELECT state,token,token_hash FROM subscription_bundles WHERE id=$1", id))
}

func (p *Aggregates) ByTokenHash(ctx context.Context, hash string) (aggregate.Bundle, error) {
	return decodeAggregate(p.pool.QueryRow(ctx, "SELECT state,token,token_hash FROM subscription_bundles WHERE token_hash=$1", hash))
}

func (p *Aggregates) List(ctx context.Context, opts aggregate.ListOptions) ([]aggregate.Bundle, error) {
	rows, err := p.pool.Query(ctx, `SELECT state,token,token_hash FROM subscription_bundles
WHERE id COLLATE "C" > $1 AND ($2='' OR external_ref=$2) ORDER BY id COLLATE "C" LIMIT $3`, opts.After, opts.ExternalRef, opts.Limit)
	if err != nil {
		return nil, errors.New("bundle listing failed")
	}
	defer rows.Close()
	out := []aggregate.Bundle{}
	for rows.Next() {
		b, err := decodeAggregate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if rows.Err() != nil {
		return nil, errors.New("bundle listing failed")
	}
	return out, nil
}

func (p *Aggregates) transaction(ctx context.Context, id string) (pgx.Tx, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, errors.New("bundle transaction failed")
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,724619582339))", id); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, errors.New("bundle lock failed")
	}
	return tx, nil
}

func (p *Aggregates) Update(ctx context.Context, id string, fn func(*aggregate.Bundle) error) (aggregate.Bundle, error) {
	tx, err := p.transaction(ctx, id)
	if err != nil {
		return aggregate.Bundle{}, err
	}
	defer tx.Rollback(context.Background())
	b, err := decodeAggregate(tx.QueryRow(ctx, "SELECT state,token,token_hash FROM subscription_bundles WHERE id=$1 FOR UPDATE", id))
	if err != nil && !errors.Is(err, aggregate.ErrNotFound) {
		return aggregate.Bundle{}, err
	}
	if err := fn(&b); err != nil {
		return aggregate.Bundle{}, err
	}
	if b.ID != id || b.Token == "" || b.TokenHash == "" {
		return aggregate.Bundle{}, aggregate.ErrInvalid
	}
	data, err := json.Marshal(b)
	if err != nil {
		return aggregate.Bundle{}, errors.New("bundle encoding failed")
	}
	_, err = tx.Exec(ctx, `INSERT INTO subscription_bundles(id,external_ref,token,token_hash,state) VALUES($1,$2,$3,$4,$5)
ON CONFLICT(id) DO UPDATE SET external_ref=EXCLUDED.external_ref,token=EXCLUDED.token,token_hash=EXCLUDED.token_hash,state=EXCLUDED.state`, id, b.ExternalRef, b.Token, b.TokenHash, data)
	if err != nil {
		return aggregate.Bundle{}, errors.New("bundle write failed")
	}
	if tx.Commit(ctx) != nil {
		return aggregate.Bundle{}, errors.New("bundle commit failed")
	}
	return b, nil
}

func (p *Aggregates) Delete(ctx context.Context, id string, expected *int64) error {
	tx, err := p.transaction(ctx, id)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	b, err := decodeAggregate(tx.QueryRow(ctx, "SELECT state,token,token_hash FROM subscription_bundles WHERE id=$1 FOR UPDATE", id))
	if err != nil {
		return err
	}
	if expected != nil && *expected != b.Revision {
		return aggregate.ErrRevision
	}
	if _, err := tx.Exec(ctx, "DELETE FROM subscription_bundles WHERE id=$1", id); err != nil {
		return errors.New("bundle delete failed")
	}
	if tx.Commit(ctx) != nil {
		return errors.New("bundle commit failed")
	}
	return nil
}

var _ aggregate.Repository = (*Aggregates)(nil)
