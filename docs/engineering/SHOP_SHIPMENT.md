# Shop Go simulated shipment

## Architecture

The Shop Go station owns shipment writes at `POST /api/stations/shop/ship`. The route uses the authenticated Shop identity, tenant database, existing `shop_orders`, `shop_shipments`, and organization-scoped `idempotency_keys` tables. Shipment creation updates the order, inserts one shipment, stores the retry result, and writes an audit event in one SQLite transaction. No external carrier or delivery API is called. The Go back office lists paid, unfulfilled orders and submits tracking information through this route.

## Boundaries

- Require an enabled Shop grant, back-office scope, and a write-enabled role. Resolve the order by both ID and organization; foreign-tenant orders are indistinguishable from missing orders. A back-office operator may fulfill any order in their own organization, matching legacy `shipOrder`'s operator permission.
- Only `payment_status='paid'` and `fulfillment_status='unfulfilled'` orders with no existing return rows may transition to shipped. A return on an unshipped order moves it to `return_hold` (or `cancelled` when all merchandise quantities were returned); it cannot use the order-level shipment API because shipment records do not identify per-line quantities. Paid shipped orders can still receive post-shipment returns without losing shipped status/tracking.
- Validate bounded order ID, tracking number, carrier, and idempotency key. Bind the scoped idempotency record to actor, role, order, tracking number, and carrier. Exact retry returns its prior shipment result; key reuse with different input conflicts.
- Use a conditional tenant-scoped order update and deterministic tenant/order shipment ID. The order transition, shipment row, idempotency response, and audit event must commit atomically. Competing requests can create at most one shipment.
- Preserve the existing schema; shipment idempotency uses the already-migrated `idempotency_keys` table. Go UI and API do not invoke the legacy Next.js action.

## Remote verification contract

Add isolated Go route tests before implementation for paid success, unpaid/refunded rejection without writes, readonly/front-office denial, same-tenant back-office operator access, cross-tenant not-found, exact and mismatched idempotency retries, validation, and concurrent shipment requests leaving one order transition and one shipment. Verify the shipment, tracking number, audit row, and idempotency row. Execute application tests only in GitHub Actions, not locally.
