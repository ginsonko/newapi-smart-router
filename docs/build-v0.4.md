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

## Hosted build into an existing draft

The manual **Build and upload existing draft** workflow uses Ubuntu 24.04, Go 1.25.1, Bun 1.2.21, Node 22.14.0 and Python 3.12. It downloads the public Full base identified by `full/reference-v0.4.json`, checks release/asset/root consistency and its SHA-256, then runs the unchanged full builder, commit finalizer and release validator. It does not use private configuration, skip host/UI tests or publish a release. The existing draft must target the exact full workflow commit SHA; dispatch on that commit's branch or tag, not on a different checkout. Its release tag must match the builder version; an existing Git tag must resolve to that same commit.

Inputs are `release_id`, `release_tag` and `expected_metadata`. The last is a JSON object mapping only `RELEASE-MANIFEST.json` and/or `SHA256SUMS` to the independently verified SHA-256 of existing draft metadata. Use `{}` to forbid replacement. Do not include credentials. The only API credential is the ephemeral `GITHUB_TOKEN`; write access is scoped to the job and the token is passed to the uploader step, not the build subprocesses. Checkout does not persist credentials.

The uploader validates all local artifacts and preflights the complete remote inventory before mutation. Same-name, same-size, same-digest uploaded assets are retained. Conflicting binaries, archives or SBOM are never overwritten. Only the two metadata names may be replaced, and only after their current digest matches the explicit expected value and draft identity is rechecked. Metadata is uploaded last. It never creates a release, changes a tag/target or clears draft status.

A POST runs once per asset. If its response is uncertain, the uploader rereads the draft asset inventory and digest with bounded waits; it does not repeat the POST. Missing, partial or conflicting state ends the run with a report for manual reconciliation. Never blindly restart a failed upload. Metadata deletion/upload is not atomic: a failure may leave a draft missing metadata, which must be reconciled before a deliberate new run. GitHub provides no conditional asset deletion or draft lock; do not edit or publish the draft concurrently. The workflow serializes its own runs for the same release ID.

Build outputs and receipts are saved with `actions/upload-artifact`, including failure evidence. Only after the hosted full build and its release checks pass does the manifest record `github_hosted_build_and_tests` and `build_run_url`. This does not claim successful upload, whole-workflow success or `remote_ci`: mainline must independently verify those through GitHub API results. A successful upload reports `DRAFT_ASSETS_VERIFIED_NOT_PUBLISHED`. Remote Linux-built hashes may differ from a prior Windows build; publish a consistent set from this runner, never mix old checksums with new payloads. Mainline owns final tag/commit checks, independent downloads and publication.
