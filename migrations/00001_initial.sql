-- +goose Up
CREATE TABLE subscriptions (
    id BIGSERIAL PRIMARY KEY,
    bedolaga_subscription_id TEXT NOT NULL UNIQUE,
    bedolaga_user_id TEXT NULL,
    remnawave_user_id BIGINT NULL,
    remnawave_short_uuid TEXT NULL,
    tariff_key TEXT NOT NULL,
    subscription_start_at TIMESTAMPTZ NOT NULL,
    subscription_end_at TIMESTAMPTZ NOT NULL,
    subscription_status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_synced_at TIMESTAMPTZ NULL,
    CONSTRAINT subscriptions_valid_range CHECK (subscription_end_at > subscription_start_at),
    CONSTRAINT subscriptions_valid_user_id CHECK (remnawave_user_id IS NULL OR remnawave_user_id > 0)
);

CREATE TABLE quota_periods (
    id BIGSERIAL PRIMARY KEY,
    subscription_id BIGINT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    cycle_index BIGINT NOT NULL,
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT quota_periods_valid_range CHECK (end_at > start_at),
    CONSTRAINT quota_periods_valid_index CHECK (cycle_index >= 0),
    UNIQUE (subscription_id, cycle_index),
    UNIQUE (subscription_id, start_at)
);

CREATE TABLE quota_pool_states (
    id BIGSERIAL PRIMARY KEY,
    quota_period_id BIGINT NOT NULL REFERENCES quota_periods(id) ON DELETE CASCADE,
    pool_key TEXT NOT NULL,
    limit_bytes BIGINT NOT NULL,
    used_bytes_high_water BIGINT NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    last_access_decision TEXT NOT NULL,
    accounting_squad_uuid TEXT NOT NULL,
    exhausted_at TIMESTAMPTZ NULL,
    last_usage_check_at TIMESTAMPTZ NULL,
    last_good_usage_at TIMESTAMPTZ NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT quota_pool_states_positive_limit CHECK (limit_bytes > 0),
    CONSTRAINT quota_pool_states_nonnegative_usage CHECK (used_bytes_high_water >= 0),
    CONSTRAINT quota_pool_states_state CHECK (state IN ('ACTIVE', 'WARNING', 'EXHAUSTED', 'DEGRADED', 'MISCONFIGURED')),
    CONSTRAINT quota_pool_states_decision CHECK (last_access_decision IN ('PRESENT', 'ABSENT')),
    UNIQUE (quota_period_id, pool_key)
);

CREATE TABLE action_log (
    id BIGSERIAL PRIMARY KEY,
    subscription_id BIGINT NULL REFERENCES subscriptions(id) ON DELETE SET NULL,
    remnawave_user_id BIGINT NULL,
    pool_key TEXT NULL,
    action TEXT NOT NULL,
    previous_state TEXT NULL,
    desired_state TEXT NULL,
    request_correlation_id TEXT NULL,
    success BOOLEAN NOT NULL,
    error_code TEXT NULL,
    error_message TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE quota_adjustments (
    id BIGSERIAL PRIMARY KEY,
    subscription_id BIGINT NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    pool_key TEXT NOT NULL,
    bytes_delta BIGINT NOT NULL,
    reason TEXT NOT NULL,
    external_reference TEXT NULL,
    valid_from TIMESTAMPTZ NULL,
    valid_until TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT quota_adjustments_valid_range CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until > valid_from)
);

CREATE INDEX quota_periods_subscription_time_idx ON quota_periods (subscription_id, start_at, end_at);
CREATE INDEX quota_pool_states_state_idx ON quota_pool_states (state, updated_at);
CREATE INDEX action_log_created_at_idx ON action_log (created_at);

-- +goose Down
DROP TABLE quota_adjustments;
DROP TABLE action_log;
DROP TABLE quota_pool_states;
DROP TABLE quota_periods;
DROP TABLE subscriptions;

