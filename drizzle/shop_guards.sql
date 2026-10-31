CREATE UNIQUE INDEX IF NOT EXISTS idempotency_scope_unique ON idempotency_keys(organization_id, key, endpoint);
--> statement-breakpoint
CREATE TRIGGER IF NOT EXISTS shop_stock_nonnegative BEFORE UPDATE OF stock_quantity ON shop_variants WHEN NEW.stock_quantity < 0 BEGIN SELECT RAISE(ABORT, 'Insufficient stock'); END;
--> statement-breakpoint
CREATE TABLE IF NOT EXISTS demo_seed_states (organization_id TEXT PRIMARY KEY NOT NULL, ready_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP);
