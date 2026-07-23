# Release status

This tree is the public source repository for `v0.1.0-alpha.1`.

| Form | Runnable | Current status | Production claim |
|---|---:|---|---|
| Full compatibility distribution | Yes, after release gates | Public Alpha reference snapshot | No |
| Certified Bridge Add-on | Yes, after host integration | rc.20-based reference-fork candidate; no official revision certified | No |
| Custom Fork Integration Kit | Tools are runnable; generated integration is a candidate | Alpha | No |
| Agent Parts Kit | No | Development Kit / Not runnable | No |

The public Alpha deliberately stops before a Stable claim. New API
`v1.0.0-rc.21` was the latest release observed during the 2026-07-23 network
audit. This package's Bridge candidate was extracted from an rc.20-based local
reference fork at `6ce7305`; it is not a certification for the official rc.20
tag or rc.21, and it fails closed on unknown revisions. The following gates
remain mandatory before Stable:

- certify an exact current upstream New API revision and re-run the Hook drift audit;
- freeze and certify protocol-aware outcome validation, including empty and
  pseudo-success responses;
- complete SQLite, MySQL, and PostgreSQL migration and transaction tests;
- complete install, upgrade, rollback, uninstall, and purge round trips;
- validate on at least one non-production external New API site;
- produce signed images, a complete image SBOM, checksums, and reproducible
  remote build receipts.

Community use, modification and redistribution follow AGPL-3.0-only. Closed
source or proprietary commercial licensing for maintainer-owned Smart Router
material is described in `COMMERCIAL-LICENSE.md`; upstream rights are excluded.

`Full Parity` is a machine-checked capability claim, not a synonym for
"compiled successfully". See `parts/spec/parity-manifest.schema.json`.
