# Build v0.4.0-alpha.1

Alpha version entry: [v0.4.0-alpha.1](https://github.com/ginsonko/newapi-smart-router/releases/tag/v0.4.0-alpha.1). This guide describes local build and maintainer binding commands, not a claim that publication or remote CI has completed.

Prerequisites: Python 3.11 or newer with PyYAML and jsonschema, Go 1.25.1, Node.js and Bun. The Full manifest pins the published base archive and every overlay file by SHA-256. No private checkout or private runtime configuration is required.

```sh
python scripts/build_v04.py --base-full-archive base-full-source.zip --output ../build/v0.4 --go /path/to/go --bun /path/to/bun
python scripts/validate_release.py --release-root ../build/v0.4
python scripts/secret_scan.py --root . --root ../build/v0.4/full-source --archive-dir ../build/v0.4/artifacts --json ../build/privacy.json
```

Download `newapi-smart-router-v0.3.0-alpha.1-full-source.zip` from the existing v0.3 release. Its required SHA-256 is `c79848fbc5a34aaa4f5086c07d57f86819eb022fed7b6efa87904522fd95c294`.

The builder starts with a fresh output directory, applies the public overlay and removal manifest, installs the locked frontend dependencies, builds both themes, runs standalone core/Bridge tests and host tests, builds a Linux amd64 binary, packages the four forms plus documentation, and emits SHA256SUMS, an SPDX source SBOM and local receipts. Caches can be placed outside the source using `GOCACHE`, `GOTMPDIR` and `BUN_INSTALL_CACHE_DIR`. Offline reuse of a populated Go module cache is supported through `GOPROXY=off`.

Reproducible means the same selected source and locked dependencies produce matching source archives with fixed ZIP timestamps. Build timing and machine-specific logs are excluded from the packaged source. Remote CI and signed container reproducibility remain unverified until separately executed.

To run the host, extract the Full source and use `go run .`, or run the matching Linux binary in an isolated environment. Supply database, session secrets and upstream channels through the normal New API configuration. The Generic configuration example contains no operational credentials or preconfigured channels. Configure media storage and normal upstream routes before exercising async image jobs. Worker and web processes should use the same artifact.

## Schema upgrade and rollback

**Do not run `bin/migration_v0.3-v0.4.sql` as a Smart Router v0.4 upgrade.** This filename belongs to an older upstream host migration, including historical model-permission changes; its version numbers are unrelated to this Smart Router release. Other historical `bin/migration_*.sql` files are not this release's upgrade instructions either. They are preserved as upstream history, not an operator runbook.

1. Back up the database and validate the upgrade in an isolated copy. Stop or quiesce new policy writes and image-job dispatch while aligning the schema and processes.
2. Use the host's normal database initialization / `AutoMigrate` path on the designated migration-capable Web/master instance. It adds `tokens.routing_policy_memory` as an independent nullable TEXT column without a NOT NULL or DEFAULT requirement. Verify that column exists before enabling the new UI/API writers; merely replacing an old Worker does not establish migration success.
3. Run Web and Worker from the same new artifact, then resume traffic and jobs after verifying their version/artifact identity. Older Workers do not read policy memory, but this is not permission for an unvalidated mixed-version async-job rollout.
4. For rollback, coordinate Web and Worker back to a matching known-good artifact and retain the new nullable column, remembered policies and durable job/accounting records. Do not drop the column, clear memory, replay unknown provider attempts or run a reverse historical migration as a shortcut. Restore a database backup only as an explicit separately reviewed recovery operation.

See [the memory and image-job contracts](v0.4-contract-delta.md). These are upgrade instructions, not evidence that a live migration, three-database round trip or production cutover has been performed.

## Maintainer commit binding

`scripts/build_release.py` is an alias for the current `build_v04.py` entry point; its retained `legacy_main` and v0.2/v0.3 base helpers are historical implementation, not the current CLI. Both entry points emit `.smart-router-release-root` with the same release identity imported by the finalizer and validators.

After independent review and CI, the authorized maintainer binds the candidate to the exact real repository commit. Do not substitute a synthetic test hash or invoke this step on an older candidate lacking the current marker:

```sh
python scripts/finalize_public_release.py --release-root ../build/v0.4 --repository-commit <FULL_PUBLIC_COMMIT_SHA>
python scripts/validate_release.py --release-root ../build/v0.4
```

The finalizer rejects a mismatched marker, manifest release, repository origin or malformed commit before modifying artifacts. It updates the manifest and SHA256SUMS, and sets validation pending; the subsequent validator must pass again. It does not create or push a commit, check GitHub CI, publish a release, or certify a Bridge revision. Keep the rebuilt release directory and its repository scripts together during this handoff.
