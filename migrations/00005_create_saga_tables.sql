-- +goose Up
-- SQL in section 'Up' is executed when this migration is applied

CREATE TABLE IF NOT EXISTS order_sagas (
    saga_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL UNIQUE,
    current_state VARCHAR(64) NOT NULL,
    payload JSONB NOT NULL,
    retry_count INT NOT NULL DEFAULT 0,
    timeout_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_order_sagas_state_timeout ON order_sagas(current_state, timeout_at);

CREATE TABLE IF NOT EXISTS order_saga_logs (
    id BIGSERIAL PRIMARY KEY,
    saga_id UUID NOT NULL REFERENCES order_sagas(saga_id) ON DELETE CASCADE,
    from_state VARCHAR(64) NOT NULL,
    to_state VARCHAR(64) NOT NULL,
    event_type VARCHAR(128) NOT NULL,
    event_id VARCHAR(128) NOT NULL,
    error_detail TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_order_saga_logs_saga_id ON order_saga_logs(saga_id);

-- +goose Down
-- SQL in section 'Down' is executed when this migration is rolled back

DROP TABLE IF EXISTS order_saga_logs CASCADE;
DROP TABLE IF EXISTS order_sagas CASCADE;
