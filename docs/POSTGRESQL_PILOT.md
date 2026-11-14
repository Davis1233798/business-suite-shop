# Shop PostgreSQL pilot

## Current scope

The Go Shop application can use PostgreSQL by setting `SUITE_DATABASE_URL`.
When it is unset, the existing SQLite path (`SUITE_DB`, defaulting to
`.data/shop-repository.sqlite`) remains active. The PostgreSQL schema is an
additive, idempotent version 2 migration in
`backend/migrations/shop_postgres_initial.sql`. Login, demo fixture seeding,
Shop catalog and workspace reads, checkout, payment, shipment, refunds,
after-sales, operations snapshots, print data, and the traffic rehearsal use
the database selected at application startup.

The checkout lab is a separate in-memory SQLite rehearsal. It does not read or
write authoritative Shop data. This remains an explicit local simulation
boundary until that lab has equivalent PostgreSQL coverage.

No existing SQLite business records are copied to PostgreSQL. Demo fixture
rows are seeded into the selected store when a demo workspace is initialized;
this is not a legacy data migration. Importing a restored SQLite database is a
separate, future operation that needs its own mapping, tenant reconciliation,
stock reconciliation, and rollback plan.

## Local PostgreSQL use

Use a disposable database for the integration tests because they create schema
and test rows:

```powershell
$env:SHOP_POSTGRES_TEST_URL = "postgres://shop_test:password@127.0.0.1:5432/shop_test?sslmode=disable"
go -C backend test ./... -count=1
```

To run the Go application against PostgreSQL, set `SUITE_DATABASE_URL` to a
Shop database URL before starting it. The application runs the idempotent
schema migration at startup using that URL. The current bootstrap therefore
requires DDL privileges on this connection; use an isolated pilot database
and resolve migration-role separation before a production cutover.

## Transaction boundary

Inventory, reservations, orders, payment callbacks and their outbox writes must
share one authoritative transaction store. Do not split a live checkout
across SQLite and PostgreSQL. Keep the HTTP contract and integer money
representation. Scope keys and relationships by organization. Use conditional
stock updates, serialize competing order changes, and enforce checkout retry
and payment event uniqueness in the database.

Analytics databases remain separate from checkout authority. PostgreSQL
connection pooling allows overlapping writes. The web integration acceptance
exercises login and session creation, tenant-scoped reads and search, checkout,
payment, shipping, refund, review, print, operations snapshots, business
checkout/payment/shipping, and traffic prepare/submit/drain.

## Acceptance evidence

Run the test suite once with no PostgreSQL URL to retain the SQLite baseline,
then with `SHOP_POSTGRES_TEST_URL` pointing at a disposable PostgreSQL 16
database. The real-engine tests cover concurrent checkout without overselling,
checkout replay, tenant-scoped keys, competing payment callbacks with one paid
outbox transition, failed outbox rollback, repeated migration, and data
surviving application-store reopen. The PostgreSQL web test exercises the
application storage selector and the station APIs listed above.

CI runs the transaction tests on a disposable PostgreSQL 16 service. The
native image matrix continues to verify the SQLite-backed application.

## Rollout boundary

This pilot does not migrate legacy records or enable production writes. Before
cutover, migrate an isolated restored fixture separately, reconcile tenant
counts and stock balances, keep the legacy source read-only during
reconciliation, and validate a write rollback procedure. After PostgreSQL
writes begin, rollback requires preserving or reconciling those writes before
resuming SQLite. Production deployment also needs separate migration and
runtime roles and live OCI routing, DNS, and TLS validation.

## Offline migration contract

Import a consistent SQLite snapshot into an initialized PostgreSQL Shop database. The operator must supply explicit source-to-destination organization and user mappings. Destination accounts must already exist. Preserve order references and inventory balances, and exclude credentials, sessions, and login limits.

Validate mappings and reconcile per-tenant row counts, order totals, and available and reserved quantities before committing. Apply all writes and the receipt in one transaction. A retry with the same source fingerprint and mappings returns the recorded result. An unexpected collision aborts the transaction. Keep the source snapshot available until cutover is verified.

### Offline import command

Run the Shop backend binary with --migrate-shop-sqlite --apply SNAPSHOT.
Supply the private JSON manifest on standard input with TenantMappings and
UserMappings objects. Configure SUITE_DATABASE_URL for the pre-initialized
destination and provision the mapped identities before import. Stop writes
before taking the source snapshot and keep the destination offline until
reconciliation succeeds. The command exits nonzero on failure and prints
the migration fingerprint and reconciliation result on success.

### Portfolio source boundary

The shared Portfolio database has no shop_repository_schema marker.
Before enabling it as an import source, verify its table layout explicitly.
Legacy orders omit coupon_code, and payment attempts omit actor_id and
actor_role. Preserve the historical rows using the destination defaults
for these absent fields, with no inferred coupon or payment actor.
Validate the approved organization and user mappings separately from
source schema validation. Exercise both snapshot formats against a
disposable PostgreSQL database and verify atomic rejection of an
unexpected source layout.
