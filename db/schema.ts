import { sql } from "drizzle-orm";
import { integer, sqliteTable, text, uniqueIndex } from "drizzle-orm/sqlite-core";

export const stationAccounts = sqliteTable('station_accounts', {
  userId: text('user_id').primaryKey(), organizationId: text('organization_id').notNull(),
  email: text('email').notNull(), passwordHash: text('password_hash').notNull(), disabled: integer('disabled').notNull().default(0),
}, t=>[uniqueIndex('station_accounts_email').on(t.organizationId,t.email)]);
export const stationGrants = sqliteTable('station_grants', {
  userId: text('user_id').notNull(), site: text('site').notNull(), role: text('role').notNull(),
  scope: text('scope').notNull(), enabled: integer('enabled').notNull().default(1),
},t=>[uniqueIndex('station_grants_user_site').on(t.userId,t.site)]);
export const stationSessions = sqliteTable('station_sessions', {
  tokenHash:text('token_hash').primaryKey(),userId:text('user_id').notNull(),expiresAt:integer('expires_at').notNull(),
});
export const stationLoginLimits = sqliteTable('station_login_limits', {
  key:text('key').primaryKey(),attempts:integer('attempts').notNull(),resetAt:integer('reset_at').notNull(),
});

// ==========================================
// 1. Common & Tenant Tables
// ==========================================

export const organizations = sqliteTable("organizations", {
  id: text("id").primaryKey(),
  name: text("name").notNull(),
  slug: text("slug").notNull().unique(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const users = sqliteTable("users", {
  id: text("id").primaryKey(),
  email: text("email").notNull(),
  displayName: text("display_name").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const memberships = sqliteTable("memberships", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  userId: text("user_id").notNull(),
  role: text("role").notNull().default("operator"), // admin, operator, readonly
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const auditEvents = sqliteTable("audit_events", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  actorId: text("actor_id").notNull(),
  actorRole: text("actor_role").notNull(),
  action: text("action").notNull(), // UPDATE_AMOUNT, UPDATE_ROLE, DEDUCT_STOCK, REFUND, etc.
  entityType: text("entity_type").notNull(),
  entityId: text("entity_id").notNull(),
  beforeValue: text("before_value"),
  afterValue: text("after_value"),
  reason: text("reason"),
  requestId: text("request_id"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const idempotencyKeys = sqliteTable("idempotency_keys", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  key: text("key").notNull(),
  endpoint: text("endpoint").notNull(),
  requestHash: text("request_hash").notNull(),
  responseStatus: integer("response_status").notNull(),
  responseBody: text("response_body").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const outboxEvents = sqliteTable("outbox_events", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  eventType: text("event_type").notNull(),
  payload: text("payload").notNull(),
  status: text("status").notNull().default("pending"), // pending, processed, failed
  retryCount: integer("retry_count").notNull().default(0),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

// ==========================================
// 2. MES「ForgeFlow」Tables
// ==========================================

export const erpItems = sqliteTable("erp_items", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  sku: text("sku").notNull(),
  name: text("name").notNull(),
  unit: text("unit").notNull().default("PCS"),
  boxConversionRatio: integer("box_conversion_ratio").notNull().default(1),
  category: text("category").notNull().default("general"),
  isActive: integer("is_active", { mode: "boolean" }).notNull().default(true),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const erpStockLedger = sqliteTable("erp_stock_ledger", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  itemId: text("item_id").notNull(),
  warehouseId: text("warehouse_id").notNull(),
  changeType: text("change_type").notNull(), // purchase_receipt, purchase_return, sales_shipment, transfer_in, transfer_out, adjustment
  quantityChange: integer("quantity_change").notNull(),
  balanceAfter: integer("balance_after").notNull(),
  unitCost: integer("unit_cost").notNull().default(0),
  totalValue: integer("total_value").notNull().default(0),
  referenceDoc: text("reference_doc").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const posSales = sqliteTable("pos_sales", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  receiptNumber: text("receipt_number").notNull(),
  storeId: text("store_id").notNull(),
  registerId: text("register_id").notNull(),
  shiftId: text("shift_id").notNull(),
  cashierId: text("cashier_id").notNull(),
  customerId: text("customer_id"),
  subtotal: integer("subtotal").notNull(),
  discountAmount: integer("discount_amount").notNull().default(0),
  taxAmount: integer("tax_amount").notNull().default(0),
  totalAmount: integer("total_amount").notNull(),
  tenderAmount: integer("tender_amount").notNull(),
  changeAmount: integer("change_amount").notNull().default(0),
  paymentMethod: text("payment_method").notNull().default("cash"),
  status: text("status").notNull().default("completed"), // completed, refunded, partially_refunded
  isReprint: integer("is_reprint", { mode: "boolean" }).notNull().default(false),
  idempotencyKey: text("idempotency_key"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const crmStages = sqliteTable("crm_stages", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  name: text("name").notNull(),
  position: integer("position").notNull(),
  probability: integer("probability").notNull(),
  isWon: integer("is_won", { mode: "boolean" }).notNull().default(false),
  isLost: integer("is_lost", { mode: "boolean" }).notNull().default(false),
});

export const shopCategories = sqliteTable("shop_categories", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  name: text("name").notNull(),
  slug: text("slug").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopProducts = sqliteTable("shop_products", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  name: text("name").notNull(),
  slug: text("slug").notNull(),
  description: text("description"),
  categoryId: text("category_id").notNull(),
  isActive: integer("is_active", { mode: "boolean" }).notNull().default(true),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopVariants = sqliteTable("shop_variants", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  productId: text("product_id").notNull(),
  sku: text("sku").notNull(),
  title: text("title").notNull(),
  color: text("color"),
  size: text("size"),
  price: integer("price").notNull(),
  stockQuantity: integer("stock_quantity").notNull().default(0),
  reservedQuantity: integer("reserved_quantity").notNull().default(0),
  isActive: integer("is_active", { mode: "boolean" }).notNull().default(true),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopInventoryReservations = sqliteTable("shop_inventory_reservations", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  variantId: text("variant_id").notNull(),
  sessionId: text("session_id").notNull(),
  cartId: text("cart_id"),
  quantity: integer("quantity").notNull(),
  expiresAt: text("expires_at").notNull(),
  status: text("status").notNull().default("active"), // active, consumed, expired
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopCarts = sqliteTable("shop_carts", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  sessionId: text("session_id").notNull(),
  userId: text("user_id"),
  couponCode: text("coupon_code"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopCartItems = sqliteTable("shop_cart_items", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  cartId: text("cart_id").notNull(),
  variantId: text("variant_id").notNull(),
  quantity: integer("quantity").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopPromotions = sqliteTable("shop_promotions", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  code: text("code").notNull(),
  description: text("description"),
  discountType: text("discount_type").notNull().default("percentage"), // percentage, fixed
  discountValue: integer("discount_value").notNull(),
  minSpend: integer("min_spend").notNull().default(0),
  expiresAt: text("expires_at"),
  isActive: integer("is_active", { mode: "boolean" }).notNull().default(true),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopOrders = sqliteTable("shop_orders", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  orderNumber: text("order_number").notNull(),
  userId: text("user_id"),
  customerEmail: text("customer_email").notNull(),
  shippingAddressJson: text("shipping_address_json").notNull(),
  subtotal: integer("subtotal").notNull(),
  discountAmount: integer("discount_amount").notNull().default(0),
  shippingFee: integer("shipping_fee").notNull().default(0),
  totalAmount: integer("total_amount").notNull(),
  paymentStatus: text("payment_status").notNull().default("pending"), // pending, paid, failed, refunded
  fulfillmentStatus: text("fulfillment_status").notNull().default("unfulfilled"), // unfulfilled, partially_shipped, shipped, delivered
  trackingNumber: text("tracking_number"),
  idempotencyKey: text("idempotency_key"),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopOrderItems = sqliteTable("shop_order_items", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  orderId: text("order_id").notNull(),
  variantId: text("variant_id").notNull(),
  productName: text("product_name").notNull(),
  variantTitle: text("variant_title").notNull(),
  sku: text("sku").notNull(),
  unitPrice: integer("unit_price").notNull(),
  quantity: integer("quantity").notNull(),
  lineTotal: integer("line_total").notNull(),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopPaymentAttempts = sqliteTable("shop_payment_attempts", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  orderId: text("order_id").notNull(),
  attemptReference: text("attempt_reference").notNull(),
  amount: integer("amount").notNull(),
  provider: text("provider").notNull().default("mock_gateway"),
  status: text("status").notNull().default("pending"), // pending, success, failed, timed_out
  callbackPayload: text("callback_payload"),
  actorId: text("actor_id").notNull().default(""),
  actorRole: text("actor_role").notNull().default(""),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopShipments = sqliteTable("shop_shipments", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  orderId: text("order_id").notNull(),
  trackingNumber: text("tracking_number").notNull(),
  carrier: text("carrier").notNull().default("Standard Logistics"),
  status: text("status").notNull().default("shipped"),
  shippedAt: text("shipped_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopReturns = sqliteTable("shop_returns", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  orderId: text("order_id").notNull(),
  orderItemId: text("order_item_id").notNull(),
  quantity: integer("quantity").notNull(),
  reason: text("reason").notNull(),
  status: text("status").notNull().default("requested"), // requested, approved, received, refunded
  refundAmount: integer("refund_amount").notNull().default(0),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const shopReviews = sqliteTable("shop_reviews", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull(),
  productId: text("product_id").notNull(),
  userId: text("user_id").notNull(),
  orderId: text("order_id").notNull(),
  rating: integer("rating").notNull(),
  comment: text("comment"),
  isVisible: integer("is_visible", { mode: "boolean" }).notNull().default(true),
  createdAt: text("created_at").notNull().default(sql`CURRENT_TIMESTAMP`),
});

export const demoSeedStates = sqliteTable("demo_seed_states", { organizationId: text("organization_id").primaryKey(), readyAt: text("ready_at").notNull().default(sql`CURRENT_TIMESTAMP`) });
