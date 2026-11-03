# Shop payment callback migration

## Architecture

Migrate the existing simulated payment callback into the Shop Go station. The checkout order, reservation, payment attempt and paid outbox event are owned by the Shop tenant database. This is a simulated provider; it does not charge a card or authenticate an external payment gateway. The existing TypeScript `ShopService.simulatePaymentCallback` is the behavioral reference, subject to the boundaries below.

## Boundaries

- Only an authenticated write-enabled Shop user can initiate a simulated callback for their own order; a Shop administrator may operate another user’s order within the same organization. Other tenants must receive a not-found response and readonly users must not write.
- The server reads the order amount and compares it exactly to the callback amount. A caller cannot replace server-side pricing. Reject malformed references, unsupported status, expired or refunded orders and amount mismatches before any write.
- Process payment and expiration under a transaction with conditional state changes: a successful payment consumes its active reservation and writes one paid event; expiration restores the stock once. Concurrent payment and expiration must never both succeed, restore paid stock, or mark an expired order paid. A failed payment does not undo an earlier success.
- A provider attempt reference is scoped to the organization and bound to order, amount, status and actor; an exact retry returns its prior result, while a different payload on the same reference is a conflict. Attempts, order state, reservation and outbox writes commit atomically.
- Existing legacy reservations and other systems are not modified. A pending order from the new Go checkout is eligible only if its own active reservation exists. Do not silently claim to process an unimplemented external provider callback, refund, shipment, or promotion.

## Remote tests before implementation

Add tests against a per-test isolated database and authenticated routes for: correct amount; failed then successful attempt; exact replay and mismatched replay; readonly, cross-tenant and other-user denial; duplicate and parallel successes producing one state transition and outbox event; payment racing expiration with exactly one winner and preserved stock; and untouched unrelated legacy reservations. Verify response state against persisted payment attempts, order status, reservation and inventory. Run Go race tests and application checks in GitHub Actions; no local application tests.

## Implementation and delivery

After the contract tests are in place, implement the transaction service and restricted route, connect the Shop back-office flow, and document the simulated-provider limitation. Open a PR, review the complete diff and remote checks, and merge to `main` only when those checks pass. Public deployment is tracked separately.
