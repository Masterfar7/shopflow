-- +goose Up
-- SQL in section 'Up' is executed when this migration is applied

CREATE TABLE IF NOT EXISTS inventory (
    sku VARCHAR(64) PRIMARY KEY,
    on_hand INT NOT NULL CHECK (on_hand >= 0),
    reserved INT NOT NULL DEFAULT 0 CHECK (reserved >= 0),
    version BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_inventory_reserved_le_onhand CHECK (reserved <= on_hand)
);

CREATE TABLE IF NOT EXISTS stock_reservations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id UUID NOT NULL UNIQUE,
    status VARCHAR(32) NOT NULL CHECK (status IN ('PENDING', 'COMMITTED', 'RELEASED')),
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_stock_reservations_status_expires ON stock_reservations(status, expires_at);

CREATE TABLE IF NOT EXISTS stock_reservation_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    reservation_id UUID NOT NULL REFERENCES stock_reservations(id) ON DELETE CASCADE,
    sku VARCHAR(64) NOT NULL REFERENCES inventory(sku),
    quantity INT NOT NULL CHECK (quantity > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_stock_reservation_item UNIQUE (reservation_id, sku)
);

CREATE INDEX IF NOT EXISTS idx_stock_reservation_items_res_id ON stock_reservation_items(reservation_id);
CREATE INDEX IF NOT EXISTS idx_stock_reservation_items_sku ON stock_reservation_items(sku);

-- +goose Down
-- SQL in section 'Down' is executed when this migration is rolled back

DROP TABLE IF EXISTS stock_reservation_items CASCADE;
DROP TABLE IF EXISTS stock_reservations CASCADE;
DROP TABLE IF EXISTS inventory CASCADE;
