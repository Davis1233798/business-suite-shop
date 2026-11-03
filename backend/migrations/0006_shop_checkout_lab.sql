CREATE TABLE IF NOT EXISTS shop_checkout_lab_runs (
 id TEXT PRIMARY KEY, organization_id TEXT NOT NULL, variant_id TEXT NOT NULL,
 initial_stock INTEGER NOT NULL CHECK(initial_stock BETWEEN 1 AND 1000),
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS shop_checkout_lab_orders (
 run_id TEXT NOT NULL REFERENCES shop_checkout_lab_runs(id) ON DELETE CASCADE,
 request_key TEXT NOT NULL, order_id TEXT NOT NULL UNIQUE,
 PRIMARY KEY(run_id,request_key)
);
