# Go Shop storefront slice

## Scope

Connect the existing Go/React `/shop/front` route to the Go Shop repository for product browsing, a client-side multi-item cart, coupon checkout, and the signed-in buyer's order history. Checkout uses `POST /api/stations/shop/checkout`; prices, discount, shipping, stock, tenant, owner, and order totals stay server-authoritative. The current checkout implementation supplies the fixed NT$60 shipping fee, 15-minute stock reservation, coupon validation/allocation, transaction, and idempotency.

This slice does not add guest checkout, a persistent server cart, a real payment provider, payment UI, or changes to the Next/Drizzle demo runtime. Orders remain pending until the existing back-office simulated payment UI is used.

## Identity and data boundaries

- Every request uses the existing Go `suite_session_shop` identity and enabled Shop grant. Never use or translate the legacy `demo_session_id` cookie, client role cookies, organization IDs, or user IDs.
- The Go server takes `organizationID` and `userID` from the authenticated identity. The browser submits only variant IDs, quantities, coupon, buyer contact/address, and a fresh idempotency key; it never submits price, stock, owner, tenant, totals, or shipping fee.
- `readonly` identities may browse and view their own orders, but cannot submit checkout. Write-enabled Shop front or back grants can checkout. The UI reflects this, while the API remains the authority.
- Front data is an explicit projection. Catalog exposes active product/category/variant fields needed to shop. Order rows are filtered by authenticated `user_id`; item rows are filtered through those owned orders. Do not expose customer email, shipping snapshots, payment attempts, reservation IDs, or other buyers' order/item data on the front endpoint.
- Existing back-office projections and admin-only refund policy remain unchanged. This storefront does not expose simulated payment, shipment, refund, or management controls.
- Idempotency keys are stable across transport retries for one unchanged cart/form payload and are replaced when that payload changes. A successful checkout clears the cart and rotates the key.

## Acceptance cases (added before UI implementation)

1. Go front identity gets only active catalog projections; variant availability is computed as `stock_quantity - reserved_quantity`, raw stock/reservation fields stay private, and variants under inactive products are excluded. The buyer gets only their own orders and item details.
2. A back-office identity using `view=front` is also limited to its own order and items; `view=back` continues to return the administrative datasets.
3. Anonymous and disabled/no-grant identities cannot read the storefront API; a readonly account can read but checkout is rejected; a write-enabled front account can checkout.
4. A two-variant checkout with a valid coupon creates one pending order and two item rows with server-derived totals; replaying the same key and payload returns the same order.
5. UI review checklist: product search/category filtering, available-quantity controls, coupon and delivery details, pending order status, loading/empty/error states, and Go station API calls only. This is a review item rather than an automated test.

## Verification status

The API cases are encoded in `backend/shop_storefront_test.go`. Per task instruction, no local test/build commands are run in this worktree; CI and review are the next verification stage.
