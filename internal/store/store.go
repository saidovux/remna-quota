package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/saidovux/remna-quota/internal/config"
	appmigrations "github.com/saidovux/remna-quota/migrations"
)

const leaderLockKey int64 = 0x72656d6e6171756f

type Store struct{ pool *pgxpool.Pool }

func Open(ctx context.Context, cfg config.Database) (*Store, error) {
	dsn := cfg.DSN()
	if err := migrate(ctx, dsn); err != nil {
		return nil, err
	}
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse quota database configuration: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open quota database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping quota database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open quota database for migrations: %w", err)
	}
	defer db.Close()
	goose.SetBaseFS(appmigrations.Files)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set migration dialect: %w", err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("migrate quota database: %w", err)
	}
	return nil
}

func (s *Store) Close() {
	if s != nil && s.pool != nil {
		s.pool.Close()
	}
}
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

type LeaderLock struct{ conn *pgxpool.Conn }

func (s *Store) TryLeaderLock(ctx context.Context) (*LeaderLock, bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire advisory lock connection: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", leaderLockKey).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("try advisory lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	return &LeaderLock{conn: conn}, true, nil
}

func (l *LeaderLock) Release(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return nil
	}
	var released bool
	err := l.conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", leaderLockKey).Scan(&released)
	l.conn.Release()
	l.conn = nil
	if err != nil {
		return fmt.Errorf("release advisory lock: %w", err)
	}
	if !released {
		return errors.New("advisory lock was not held")
	}
	return nil
}

func (s *Store) UpsertSubscription(ctx context.Context, sub Subscription) (int64, error) {
	if sub.BedolagaSubscriptionID == "" || sub.TariffKey == "" || sub.Status == "" || !sub.EndAt.After(sub.StartAt) {
		return 0, errors.New("invalid subscription")
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
INSERT INTO subscriptions (
  bedolaga_subscription_id, bedolaga_user_id, remnawave_user_id, remnawave_short_uuid,
  tariff_key, subscription_start_at, subscription_end_at, subscription_status, last_synced_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (bedolaga_subscription_id) DO UPDATE SET
  bedolaga_user_id=EXCLUDED.bedolaga_user_id,
  remnawave_user_id=EXCLUDED.remnawave_user_id,
  remnawave_short_uuid=EXCLUDED.remnawave_short_uuid,
  tariff_key=EXCLUDED.tariff_key,
  subscription_start_at=EXCLUDED.subscription_start_at,
  subscription_end_at=EXCLUDED.subscription_end_at,
  subscription_status=EXCLUDED.subscription_status,
  last_synced_at=EXCLUDED.last_synced_at,
  updated_at=now()
RETURNING id`, sub.BedolagaSubscriptionID, sub.BedolagaUserID, sub.RemnawaveUserID, sub.RemnawaveShortUUID, sub.TariffKey, sub.StartAt.UTC(), sub.EndAt.UTC(), sub.Status, sub.LastSyncedAt.UTC()).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("upsert subscription: %w", err)
	}
	return id, nil
}

func (s *Store) EnsurePeriod(ctx context.Context, subscriptionID, cycleIndex int64, start, end time.Time, pools []PoolSeed) (int64, error) {
	if subscriptionID <= 0 || cycleIndex < 0 || !end.After(start) || len(pools) == 0 {
		return 0, errors.New("invalid quota period")
	}
	poolKeys := make([]string, 0, len(pools))
	seenPoolKeys := make(map[string]struct{}, len(pools))
	for _, seed := range pools {
		if seed.Key == "" || seed.LimitBytes <= 0 || seed.AccountingSquadUUID == "" {
			return 0, errors.New("invalid pool seed")
		}
		if _, exists := seenPoolKeys[seed.Key]; exists {
			return 0, errors.New("duplicate pool seed")
		}
		seenPoolKeys[seed.Key] = struct{}{}
		poolKeys = append(poolKeys, seed.Key)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin ensure period: %w", err)
	}
	defer tx.Rollback(ctx)
	var periodID int64
	err = tx.QueryRow(ctx, `INSERT INTO quota_periods (subscription_id,cycle_index,start_at,end_at) VALUES ($1,$2,$3,$4)
ON CONFLICT (subscription_id,cycle_index) DO UPDATE SET end_at=EXCLUDED.end_at RETURNING id`, subscriptionID, cycleIndex, start.UTC(), end.UTC()).Scan(&periodID)
	if err != nil {
		return 0, fmt.Errorf("ensure quota period: %w", err)
	}
	for _, seed := range pools {
		_, err = tx.Exec(ctx, `INSERT INTO quota_pool_states (quota_period_id,pool_key,limit_bytes,state,last_access_decision,accounting_squad_uuid)
VALUES ($1,$2,$3,'ACTIVE',$4,$5)
ON CONFLICT (quota_period_id,pool_key) DO UPDATE SET limit_bytes=EXCLUDED.limit_bytes,accounting_squad_uuid=EXCLUDED.accounting_squad_uuid,updated_at=now()`, periodID, seed.Key, seed.LimitBytes, seed.InitialDecision, seed.AccountingSquadUUID)
		if err != nil {
			return 0, fmt.Errorf("ensure pool state %q: %w", seed.Key, err)
		}
	}
	// A Bedolaga tariff change can remove a pool from the current quota cycle.
	// Retaining that row would leave stale PRESENT state in metrics and operator
	// queries even though reconciliation has removed the squad membership.
	if _, err = tx.Exec(ctx, `DELETE FROM quota_pool_states WHERE quota_period_id=$1 AND NOT (pool_key = ANY($2::text[]))`, periodID, poolKeys); err != nil {
		return 0, fmt.Errorf("remove obsolete quota pool states: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit ensure period: %w", err)
	}
	return periodID, nil
}

func (s *Store) PoolState(ctx context.Context, periodID int64, poolKey string) (PoolState, error) {
	var p PoolState
	err := s.pool.QueryRow(ctx, `SELECT id,quota_period_id,pool_key,limit_bytes,used_bytes_high_water,state,last_access_decision,accounting_squad_uuid,exhausted_at,last_usage_check_at,last_good_usage_at,updated_at FROM quota_pool_states WHERE quota_period_id=$1 AND pool_key=$2`, periodID, poolKey).Scan(&p.ID, &p.QuotaPeriodID, &p.PoolKey, &p.LimitBytes, &p.UsedBytesHighWater, &p.State, &p.LastAccessDecision, &p.AccountingSquadUUID, &p.ExhaustedAt, &p.LastUsageCheckAt, &p.LastGoodUsageAt, &p.UpdatedAt)
	if err != nil {
		return PoolState{}, fmt.Errorf("get pool state: %w", err)
	}
	return p, nil
}

func (s *Store) RecordUsage(ctx context.Context, poolStateID, observedBytes int64, state string, checkedAt time.Time) error {
	if observedBytes < 0 {
		return errors.New("observed usage cannot be negative")
	}
	result, err := s.pool.Exec(ctx, `UPDATE quota_pool_states SET used_bytes_high_water=GREATEST(used_bytes_high_water,$2),state=$3,last_usage_check_at=$4,last_good_usage_at=$4,exhausted_at=CASE WHEN $3='EXHAUSTED' THEN COALESCE(exhausted_at,$4) ELSE NULL END,updated_at=now() WHERE id=$1`, poolStateID, observedBytes, state, checkedAt.UTC())
	if err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("pool state not found")
	}
	return nil
}

func (s *Store) RecordUsageFailure(ctx context.Context, poolStateID int64, checkedAt time.Time) error {
	result, err := s.pool.Exec(ctx, `UPDATE quota_pool_states SET state='DEGRADED',last_usage_check_at=$2,updated_at=now() WHERE id=$1`, poolStateID, checkedAt.UTC())
	if err != nil {
		return fmt.Errorf("record usage failure: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("pool state not found")
	}
	return nil
}

func (s *Store) RecordDecision(ctx context.Context, poolStateID int64, decision string) error {
	result, err := s.pool.Exec(ctx, `UPDATE quota_pool_states SET last_access_decision=$2,updated_at=now() WHERE id=$1`, poolStateID, decision)
	if err != nil {
		return fmt.Errorf("record access decision: %w", err)
	}
	if result.RowsAffected() != 1 {
		return errors.New("pool state not found")
	}
	return nil
}

func (s *Store) AppendAction(ctx context.Context, entry ActionLog) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO action_log (subscription_id,remnawave_user_id,pool_key,action,previous_state,desired_state,request_correlation_id,success,error_code,error_message) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, entry.SubscriptionID, entry.RemnawaveUserID, entry.PoolKey, entry.Action, entry.PreviousState, entry.DesiredState, entry.RequestCorrelationID, entry.Success, entry.ErrorCode, entry.ErrorMessage)
	if err != nil {
		return fmt.Errorf("append action log: %w", err)
	}
	return nil
}
