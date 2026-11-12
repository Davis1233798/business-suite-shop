# Shop independent OCI delivery

Shop main currently publishes a revision-tagged container after validation.
The next release step is a private OCI candidate on the existing host with
separate state. The Portfolio ingress remains the public entry point until the
candidate passes route and data acceptance.

## Feature boundary

Publish an image for the host architecture. Deploy a pinned digest through a
restricted release command and retain the preceding revision for rollback.
The candidate listens privately and owns a separate persistent volume.
Initialize a candidate with fictional fixtures for acceptance. Any migration
of existing orders requires reconciliation of order, payment and shipment
references before changing the public route.

## Acceptance

Verify the health revision matches the candidate, unauthorized writes are
rejected, and a test order survives replacement. Verify rollback and
demonstrate a successful restore into an isolated volume. Verify public HTTPS
routing and the live revision after the ingress change.
