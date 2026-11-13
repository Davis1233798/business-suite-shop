# Shop PostgreSQL pilot

## Architecture

Use PostgreSQL as the Shop Web transaction store on OCI. Give Shop its own database, migration role and runtime role. Keep the analytics databases separate from checkout authority. SQLite remains available for existing local development and checkout lab fixtures until those paths have equivalent PostgreSQL coverage.

## Program boundary

Inventory, reservations, orders, payment callbacks and their outbox writes must share one authoritative transaction store. Do not split a live checkout across SQLite and PostgreSQL. Keep the HTTP contract and integer money representation. Scope keys and relationships by organization. Migrations run separately from the runtime role.

Replace SQLite bootstrap, schema introspection, insert-ignore syntax, and text timestamp comparisons at the storage boundary. A PostgreSQL connection pool permits overlapping writes. Preserve conditional stock updates, serialize competing changes to an order, and enforce uniqueness for checkout retries and payment event IDs in the database.

## Tests before implementation

Run against real PostgreSQL and the current SQLite baseline. Verify concurrent checkout cannot oversell, replay of an accepted checkout creates one order, competing payment callbacks produce one effective transition, and a failed outbox write rolls back the transaction. Include another tenant using the same request key and verify that stock and orders stay isolated. Run the migration twice and verify restored data, row reconciliation and application restart.

## Rollout

Validate a restored isolated fixture before migrating legacy records. Reconcile tenant counts and stock balances before enabling writes. Keep the legacy source read-only during reconciliation. After enabling PostgreSQL writes, rolling back requires preserving or reconciling those writes before resuming SQLite.

Status: storage dependencies inventoried. PostgreSQL implementation and real-engine acceptance are pending. The local Docker client cannot currently reach its Linux engine.
