# Shop Go checkout coupons

## Architecture

The Go checkout owns promotion validation and pricing inside the Shop tenant database transaction. It reads active product and variant prices, validates at most one coupon against the same organization, persists the coupon code and order discount on `shop_orders`, allocates the discount to item lines, and records discounted `line_total` values. Inventory reservations, order/item writes and the idempotency response commit together. The existing legacy checkout remains the pricing reference; both use a fixed TWD 60 shipping fee.

## Boundaries

- The checkout route requires an authenticated write-enabled Shop identity. Product, promotion, order and inventory queries are organization scoped; a coupon belonging to another organization behaves as unknown.
- The client supplies variant IDs, quantities and an optional coupon code only. The server resolves all prices and rejects missing/inactive products, invalid quantities, malformed addresses, and stock shortfalls.
- A coupon must exist, be active, be unexpired, have a supported nonnegative fixed or percentage discount, and meet `min_spend` against the raw merchandise subtotal. The discount is rounded like legacy `Math.round` for percentage coupons, then capped at the merchandise subtotal. Coupons exclude shipping and cannot be combined.
- Legacy Shop pricing always charges TWD 60 shipping and defines no free-shipping threshold or promotion redemption-count limit. This migration does not invent either rule.
- Multi-item coupon discount is distributed proportionally to raw line totals using integer floor shares. Any rounding remainder is applied from the final line backward only to lines with undiscounted value remaining; zero-priced tail lines cannot become negative. Persisted line totals therefore sum exactly to subtotal minus discount. Refunds use those already-discounted line totals and the existing cumulative quantity rounding, so returned units receive their share of the coupon discount and never the pre-discount price.
- The request fingerprint binds tenant-scoped idempotency to user, normalized items, email, address and coupon. Exact retries return the stored order; changing the coupon or any other input with the same key conflicts.
- Fresh databases include the nullable `shop_orders.coupon_code` snapshot. Existing version 2 databases receive it through `shop_coupon_migration.sql`; the repository marker advances to version 3 transactionally.

## Remote verification contract

Add isolated Go route tests before implementation for server-side percentage and fixed coupon pricing, exact rounding and line allocation with zero-priced tail items, discount cap at merchandise subtotal, fixed shipping amount, minimum spend, expiry, disabled/unknown/foreign-tenant coupons, exact retry and same-key changed-coupon conflict, multi-item stock reservation, payment/expiry races consuming or restoring every reservation exactly once, and partial refunds computed from discounted line totals. Cover legacy single-item retry fingerprints, fresh schema creation, version 1 and version 2 upgrades, and repeated database opening. Run Go race tests and application checks in GitHub Actions; do not run local application tests.
