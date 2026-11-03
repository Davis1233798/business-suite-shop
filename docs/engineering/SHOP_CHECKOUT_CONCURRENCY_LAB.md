# Shop checkout concurrency lab

## Architecture

The Shop Go API exposes `/api/stations/shop/checkout-lab` as a bounded demonstration path through the real Shop order tables. `prepare` creates a synthetic product and variant in the caller's organization and records the run boundary. `checkout` accepts one unit per idempotency key, reads the current server-side variant price, conditionally decrements only that run's variant, and writes `shop_orders`, `shop_order_items`, and the run-to-order idempotency mapping in one SQLite transaction.

The operation requires an enabled Shop back-office grant and a write-enabled role. Every read and write is organization scoped. Readonly users cannot prepare or submit. A run has capacity 1–1000 and the UI bounds burst sizes. This lab intentionally writes real Shop orders; its synthetic product and orders are demo records visible in the organization's back office. It never chooses or decrements an existing catalog variant.

This is a focused checkout migration slice, not a port of all legacy Shop actions, payment callbacks, shipping, refunds, coupons, or reservation rules. SQLite uses one connection here, so measured request rate is a correctness/contention observation, not a production throughput claim.

## Program boundaries

- The lab API is separate from general legacy route compatibility and is served by the Go station runtime.
- The run owns the generated variant; a checkout cannot submit an arbitrary variant ID.
- `shop_checkout_lab_orders` scopes idempotency keys to run; successful outcomes reference real `shop_orders` and `shop_order_items` rows.
- Inventory decrement uses a guarded `stock_quantity > 0` update and the shop nonnegative-stock trigger.
- Transaction rollback prevents order/item writes if any later insert fails.
- Back-office UI is rendered only for a signed-in back-office identity; the server independently enforces grants and readonly policy.

## Verification contract

Remote Go tests must show concurrent buyers cannot oversell, repeated keys create one order, exhausted stock returns sold-out, created orders use the server price, another organization cannot access the run, and readonly users cannot write. CI is authoritative for execution; do not report local test evidence for this change.
