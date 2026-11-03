# Shop checkout concurrency lab

## Architecture

The Shop Go API exposes `/api/stations/shop/checkout` as the focused general checkout operation and `/api/stations/shop/checkout-lab` as a bounded demonstration client of that same transaction service. General checkout accepts a variant ID and quantity, but reads product, price, and available stock from the server database; it applies an organization-scoped idempotency key and writes `shop_orders`, `shop_order_items`, and the stock decrement in one SQLite transaction.

`prepare` for the lab creates a synthetic product and variant in a dedicated in-memory SQLite database per run, using the same Shop schema and checkout transaction function. A run database is never the workspace's production/demo database. It is capped at 10 active runs per organization and 100 total, expires after 30 minutes, and is closed when removed. The lab's successful checkouts therefore create actual Shop order and order-item rows in its isolated database without leaving synthetic records in the workspace.

General checkout requires an enabled Shop grant and a write-enabled role. Lab operations require an enabled back-office grant and a write-enabled role. Every read and write is organization scoped. Idempotency is scoped to organization, endpoint, and authenticated user, so another account cannot replay the cached order response. Inventory decrement requires `stock_quantity - reserved_quantity >= quantity`, matching the legacy checkout's available-stock rule. The transaction also creates the order's 15-minute inventory reservation and applies the legacy 60-unit shipping fee. Readonly users cannot submit. A run has capacity 1–1000 and the UI bounds burst sizes. The lab never chooses or decrements an existing catalog variant and never persists its synthetic catalog or orders into the workspace database.

This is a focused checkout migration slice, not a port of all legacy Shop actions, payment callbacks, shipping, refunds, coupons, or reservation rules. SQLite uses one connection here, so measured request rate is a correctness/contention observation, not a production throughput claim.

## Program boundaries

- The lab API is separate from general legacy route compatibility and is served by the Go station runtime.
- The run owns the generated variant; a lab checkout cannot submit an arbitrary variant ID.
- The shared checkout service scopes idempotency to organization and endpoint; successful lab outcomes reference actual `shop_orders` and `shop_order_items` rows in the run database.
- Inventory decrement uses a guarded available-stock update and the shop nonnegative-stock trigger.
- Transaction rollback prevents order/item writes if any later insert fails.
- Back-office UI is rendered only for a signed-in back-office identity; the server independently enforces grants and readonly policy.

## Verification contract

Remote Go tests must show concurrent buyers cannot oversell, repeated keys create one order, exhausted stock returns sold-out, created orders use the server price, another organization cannot access the run, and readonly users cannot write. CI is authoritative for execution; do not report local test evidence for this change.
