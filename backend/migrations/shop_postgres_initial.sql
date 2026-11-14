CREATE TABLE IF NOT EXISTS shop_repository_schema (
  version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS organizations (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  slug TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  email TEXT NOT NULL,
  display_name TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS shop_categories (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (organization_id, id),
  UNIQUE (organization_id, slug)
);

CREATE TABLE IF NOT EXISTS shop_products (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  name TEXT NOT NULL,
  slug TEXT NOT NULL,
  description TEXT,
  category_id TEXT NOT NULL,
  is_active INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (organization_id, id),
  UNIQUE (organization_id, slug),
  FOREIGN KEY (organization_id, category_id) REFERENCES shop_categories(organization_id, id)
);

CREATE TABLE IF NOT EXISTS shop_variants (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  product_id TEXT NOT NULL,
  sku TEXT NOT NULL,
  title TEXT NOT NULL,
  color TEXT,
  size TEXT,
  price BIGINT NOT NULL,
  stock_quantity BIGINT NOT NULL DEFAULT 0 CHECK (stock_quantity >= 0),
  reserved_quantity BIGINT NOT NULL DEFAULT 0 CHECK (reserved_quantity >= 0),
  is_active INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (organization_id, id),
  UNIQUE (organization_id, sku),
  FOREIGN KEY (organization_id, product_id) REFERENCES shop_products(organization_id, id)
);

ALTER TABLE shop_variants ALTER COLUMN stock_quantity TYPE BIGINT USING stock_quantity::BIGINT;
ALTER TABLE shop_variants ALTER COLUMN reserved_quantity TYPE BIGINT USING reserved_quantity::BIGINT;

CREATE TABLE IF NOT EXISTS shop_promotions (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  code TEXT NOT NULL,
  description TEXT,
  discount_type TEXT NOT NULL DEFAULT 'percentage',
  discount_value BIGINT NOT NULL,
  min_spend BIGINT NOT NULL DEFAULT 0,
  expires_at TEXT,
  is_active INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (organization_id, code)
);

CREATE TABLE IF NOT EXISTS shop_orders (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  order_number TEXT NOT NULL,
  user_id TEXT,
  customer_email TEXT NOT NULL,
  coupon_code TEXT,
  shipping_address_json TEXT NOT NULL,
  subtotal BIGINT NOT NULL,
  discount_amount BIGINT NOT NULL DEFAULT 0,
  shipping_fee BIGINT NOT NULL DEFAULT 0,
  total_amount BIGINT NOT NULL,
  payment_status TEXT NOT NULL DEFAULT 'pending',
  fulfillment_status TEXT NOT NULL DEFAULT 'unfulfilled',
  tracking_number TEXT,
  idempotency_key TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (organization_id, id),
  UNIQUE (organization_id, order_number)
);

CREATE TABLE IF NOT EXISTS shop_order_items (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL,
  order_id TEXT NOT NULL,
  variant_id TEXT NOT NULL,
  product_name TEXT NOT NULL,
  variant_title TEXT NOT NULL,
  sku TEXT NOT NULL,
  unit_price BIGINT NOT NULL,
  quantity INTEGER NOT NULL,
  line_total BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (organization_id, order_id) REFERENCES shop_orders(organization_id, id),
  FOREIGN KEY (organization_id, variant_id) REFERENCES shop_variants(organization_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS shop_order_items_org_id_unique ON shop_order_items(organization_id, id);

CREATE TABLE IF NOT EXISTS shop_inventory_reservations (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL,
  variant_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  cart_id TEXT,
  quantity INTEGER NOT NULL,
  expires_at TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (organization_id, variant_id) REFERENCES shop_variants(organization_id, id),
  FOREIGN KEY (organization_id, cart_id) REFERENCES shop_orders(organization_id, id)
);

CREATE TABLE IF NOT EXISTS idempotency_keys (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  key TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  response_status INTEGER NOT NULL,
  response_body TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (organization_id, key, endpoint)
);

CREATE TABLE IF NOT EXISTS shop_payment_attempts (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL,
  order_id TEXT NOT NULL,
  attempt_reference TEXT NOT NULL,
  amount BIGINT NOT NULL,
  provider TEXT NOT NULL DEFAULT 'mock_gateway',
  status TEXT NOT NULL DEFAULT 'pending',
  callback_payload TEXT,
  actor_id TEXT NOT NULL DEFAULT '',
  actor_role TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (organization_id, order_id) REFERENCES shop_orders(organization_id, id),
  UNIQUE (organization_id, attempt_reference)
);

CREATE TABLE IF NOT EXISTS outbox_events (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  event_type TEXT NOT NULL,
  payload TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  retry_count INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS audit_events (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  actor_id TEXT NOT NULL,
  actor_role TEXT NOT NULL,
  action TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  before_value TEXT,
  after_value TEXT,
  reason TEXT,
  request_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS station_accounts (
  user_id TEXT PRIMARY KEY REFERENCES users(id),
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  email TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0,
  UNIQUE (organization_id, email),
  UNIQUE (organization_id, user_id)
);

CREATE TABLE IF NOT EXISTS station_grants (
  user_id TEXT NOT NULL REFERENCES station_accounts(user_id),
  site TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('admin','operator','readonly')),
  scope TEXT NOT NULL CHECK (scope IN ('front','back')),
  enabled INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY (user_id, site)
);

CREATE TABLE IF NOT EXISTS station_sessions (
  token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES station_accounts(user_id),
  expires_at BIGINT NOT NULL
);

CREATE INDEX IF NOT EXISTS station_sessions_user ON station_sessions(user_id);

CREATE TABLE IF NOT EXISTS station_login_limits (
  key TEXT PRIMARY KEY,
  attempts INTEGER NOT NULL,
  reset_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS shop_carts (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  session_id TEXT NOT NULL,
  user_id TEXT,
  coupon_code TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS shop_cart_items (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL,
  cart_id TEXT NOT NULL,
  variant_id TEXT NOT NULL,
  quantity INTEGER NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (organization_id, variant_id) REFERENCES shop_variants(organization_id, id)
);

CREATE TABLE IF NOT EXISTS shop_returns (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL,
  order_id TEXT NOT NULL,
  order_item_id TEXT NOT NULL,
  quantity INTEGER NOT NULL,
  reason TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'requested',
  refund_amount BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (organization_id, order_id) REFERENCES shop_orders(organization_id, id),
  FOREIGN KEY (organization_id, order_item_id) REFERENCES shop_order_items(organization_id, id)
);

CREATE TABLE IF NOT EXISTS shop_reviews (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  product_id TEXT NOT NULL,
  user_id TEXT NOT NULL REFERENCES users(id),
  order_id TEXT NOT NULL,
  rating INTEGER NOT NULL,
  comment TEXT,
  is_visible INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (organization_id, product_id) REFERENCES shop_products(organization_id, id),
  FOREIGN KEY (organization_id, order_id) REFERENCES shop_orders(organization_id, id)
);

CREATE TABLE IF NOT EXISTS shop_shipments (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL,
  order_id TEXT NOT NULL,
  tracking_number TEXT NOT NULL,
  carrier TEXT NOT NULL DEFAULT 'Standard Logistics',
  status TEXT NOT NULL DEFAULT 'shipped',
  shipped_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY (organization_id, order_id) REFERENCES shop_orders(organization_id, id),
  UNIQUE (organization_id, order_id)
);

CREATE TABLE IF NOT EXISTS traffic_runs (
  id TEXT PRIMARY KEY,
  organization_id TEXT NOT NULL REFERENCES organizations(id),
  site TEXT NOT NULL,
  capacity INTEGER NOT NULL CHECK (capacity BETWEEN 1 AND 1000),
  committed INTEGER NOT NULL DEFAULT 0 CHECK (committed >= 0 AND committed <= capacity),
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS traffic_records (
  run_id TEXT NOT NULL REFERENCES traffic_runs(id) ON DELETE CASCADE,
  request_key TEXT NOT NULL,
  sequence INTEGER NOT NULL,
  processed INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (run_id, request_key),
  UNIQUE (run_id, sequence)
);

CREATE INDEX IF NOT EXISTS shop_orders_org_created ON shop_orders(organization_id, created_at DESC);
CREATE INDEX IF NOT EXISTS shop_reservations_org_status ON shop_inventory_reservations(organization_id, status, expires_at);
CREATE INDEX IF NOT EXISTS shop_payment_attempts_order ON shop_payment_attempts(organization_id, order_id, created_at DESC);
CREATE INDEX IF NOT EXISTS shop_returns_org_order ON shop_returns(organization_id, order_id, created_at DESC);
CREATE INDEX IF NOT EXISTS shop_reviews_org_product ON shop_reviews(organization_id, product_id, created_at DESC);
CREATE INDEX IF NOT EXISTS shop_shipments_org_order ON shop_shipments(organization_id, order_id, shipped_at DESC);
CREATE INDEX IF NOT EXISTS traffic_runs_org_site_created ON traffic_runs(organization_id, site, created_at DESC);
