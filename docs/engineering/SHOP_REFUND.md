# Shop Go simulated return and refund

## Architecture

The Shop Go station owns refund writes at `POST /api/stations/shop/refund`. It uses the authenticated Shop tenant and existing `shop_orders`, `shop_order_items`, `shop_returns`, `shop_variants`, `idempotency_keys`, and `audit_events` tables. A refund transaction records a returned item quantity and integer refund amount, restores that quantity to the same tenant's variant stock, caches the retry response, and audits the change. This is simulated accounting only; no payment provider or card is contacted. The TypeScript `ShopService.refundOrderItem` is the behavioral reference.

## Boundaries

- Require an enabled Shop back-office administrator. All order, item, return, and variant reads/writes are organization scoped; a foreign-tenant order is reported as not found. This matches legacy `checkPermission(context, "admin")`.
- The order must be paid. Refunds are item based and exclude the order's shipping fee. Preserve `payment_status`, `fulfillment_status`, and tracking; paid shipped orders may be returned, matching legacy behavior.
- Require an item belonging to the given order, positive bounded quantity, non-empty bounded reason, and a bounded idempotency key. Sum prior return quantities for that tenant/item before applying the limit. Compute each refund as `floor(line_total * cumulative_qty / original_qty) - floor(line_total * previous_qty / original_qty)` so split integer refunds sum exactly to the line total.
- In one transaction, guard cumulative quantity, insert one refunded return row, restock the matching tenant variant, write an actor-bound idempotency response, and write an audit event. On exact retry return the saved result; reuse of a key for different actor or data conflicts. Concurrent returns cannot exceed the original item quantity or restock inventory twice.
- Do not change the order's payment or shipment state, restock unrelated variants, or claim to send a real payment refund.

## Remote verification contract

Add isolated Go route tests before implementation for partial refunds and cumulative rounding; full item quantity bounds; order payment/shipment state preservation and shipping-fee exclusion; admin/operator/readonly/front-office and cross-tenant access; wrong-order items; exact and conflicting idempotency retries; and concurrent over-return attempts with one committed return and one inventory restoration. Verify return rows, refund sums, stock, audit, retry response, and unchanged order state. Run tests only in GitHub Actions, not locally.
