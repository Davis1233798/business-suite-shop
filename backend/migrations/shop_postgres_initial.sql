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
  stock_quantity INTEGER NOT NULL DEFAULT 0 CHECK (stock_quantity >= 0),
  reserved_quantity INTEGER NOT NULL DEFAULT 0 CHECK (reserved_quantity >= 0),
  is_active INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE (organization_id, id),
  UNIQUE (organization_id, sku),
  FOREIGN KEY (organization_id, product_id) REFERENCES shop_products(organization_id, id)
);

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

CREATE INDEX IF NOT EXISTS shop_orders_org_created ON shop_orders(organization_id, created_at DESC);
CREATE INDEX IF NOT EXISTS shop_reservations_org_status ON shop_inventory_reservations(organization_id, status, expires_at);
CREATE INDEX IF NOT EXISTS shop_payment_attempts_order ON shop_payment_attempts(organization_id, order_id, created_at DESC);
