# Shop private OCI candidate

## Status

This tree is **CI preparation only**. It validates and publishes versioned Shop station images and prepares isolated candidate configuration; it does not provision a host, install the forced-command key, or provide a host deployment workflow. It does not authorize or perform a deployment, import portfolio data, or change public routing.

The candidate starts with an empty, independent SQLite volume and fictional Shop demo fixtures. Do not copy, mount, migrate, or restore the Portfolio database or any real orders into it. Keep the Portfolio ingress as the only public Shop entry until a separate cutover plan has reconciliation and approval.

## Architecture

- Publish a multi-architecture candidate tag from the already validated `main` images. Resolve the tag to an immutable manifest digest before any separately approved host deployment. The `-private-candidate` suffix is only a tag name; it does not set GHCR package visibility. Before a future deployment, verify the package is private and grant read access only to the deployment identity.
- Run the candidate under its own Compose project, `shop-private-candidate`, from `/srv/business-suite-shop-candidate`. It has one station service, an internal-only Docker network, and a distinct named volume, `business-suite-shop-candidate-data`.
- Bind only `127.0.0.1:18195` on the host to the station container. Do not attach the candidate to the existing Caddy/Portfolio network, add a public hostname, open a firewall port, or edit any public Caddy configuration. Acceptance uses a host-local HTTP probe or an approved SSH tunnel.
- Pin each release to `ghcr.io/davis1233798/business-suite-shop-station@sha256:...` and the exact source commit. The image must carry `org.opencontainers.image.revision` equal to the requested full commit; the host verifies that label after pulling the digest, checks host architecture, then checks the running health revision before promoting the candidate-only `current` link. Health alone is not evidence that the digest belongs to that commit.
- If the first candidate start fails, keep its volume and an `INITIALIZING` marker with the exact commit and image digest. A retry may reuse that volume only for the same pair. A different image or an unmarked existing volume is refused and needs operator-led backup and recovery; the deploy script does not delete or silently adopt it.
- Keep release files, lock, and backups under the candidate root. If a new release fails health before promotion, automated recovery restores the pre-upgrade candidate data snapshot and starts the prior image. This relies on that snapshot being complete and readable. There is no general guarantee that a prior binary can read a database already migrated by a newer binary: after a successful promotion, do not manually point `current` at an older release against the newer database. Any later rollback needs a separately reviewed, matched pre-upgrade snapshot restore into a separate candidate volume before the old image starts. It must never use the Portfolio deployment script, project, volume, database path, or edge service.
- A future host setup must protect deployment with a dedicated restricted SSH key and forced command. Do not reuse the suite-wide `OCI_DEPLOY_KEY` or its broad release command. Host access, package visibility/read access, disk capacity, and architecture need separate checks before a host workflow is designed and enabled. The checked-in SSH gateway and release script are unprovisioned preparation artifacts and must not be treated as an available deployment path.

## Synthetic acceptance

1. Start with a new candidate volume and a fixed synthetic demo workspace cookie. Use the Shop demo account and seed fixture catalog; do not use a Portfolio cookie or source database.
2. Verify the revision endpoint, unauthenticated write rejection, and private loopback binding.
3. Create one synthetic order, replace the container with the same pinned digest, then verify that the order and inventory result persist in the candidate volume.
4. Snapshot the candidate database after a clean stop, restore it to a separately named temporary candidate volume, start an isolated container from that volume, and verify the synthetic order there.
5. Inject a new-release startup failure and verify recovery restores the previous candidate revision and its pre-upgrade synthetic database snapshot. Never delete a volume as part of rollback or restore.

The publishing job rebuilds the image for each architecture and runs the synthetic lifecycle and revision-label checks against that exact local image before pushing it. It is a separate build from the validation job, so byte identity between those jobs is not asserted. The rollback harness models failed startup, validates that the exact candidate volume receives the saved bytes before the prior image starts, and verifies its health revision. It also verifies first-start failure retention and same-image retry; it does not establish application schema compatibility for an operator-initiated rollback after a successful promotion. Public HTTPS routing, DNS/firewall changes, host setup, existing orders, real payment integrations, and live revision cutover are expressly outside this preparation.
