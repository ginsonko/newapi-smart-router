# v0.4.0-alpha.1 — Alpha version overview

Generic R98 Smart Router reference retaining QuantumNous New API identity and AGPL attribution. Start with the [fixed v0.4 version page](https://github.com/ginsonko/newapi-smart-router/releases/tag/v0.4.0-alpha.1), [capabilities and migration notes](docs/v0.4-contract-delta.md), and [build/run instructions](docs/build-v0.4.md). The version link does not assert publication or a passing CI run; asset availability and build evidence must be read from that version's page, manifest, checksums and receipts.

This version adds per-Key nullable policy memory, current authorization/catalog/quote isolation, durable image jobs with refund-before-fallback, configurable one-hour image health, and reconciliation-only unknown acceptance. Private site configuration/data, shop/courses and Bot relief are excluded. The four forms retain their existing acceptance standards.

| Form | v0.4 adoption | Boundary |
|---|---|---|
| Full compatibility distribution | Generic R98 source, both themes, Worker and matching Linux binary | Alpha, not Stable; validate schema/configuration in isolation; remembered-draft editor is default-theme only |
| Certified Bridge Add-on candidate | `bridge-spi-v1alpha4`, Core and required memory/catalog/image-job Hooks | No official host revision is Certified; unknown revisions do not become certified |
| Custom Fork Integration Kit | Read-only doctor, contracts, schemas, vectors and checklist | Host integration and semantic evidence required |
| Agent Parts Kit | Synchronized reference core, specifications and fixtures | Development Kit / Not runnable |

Local build results do not establish remote CI, paid-provider settlement, production upgrade, three-database lifecycle acceptance or signed images. These remain separately evidenced obligations; no automatic certification or production claim follows from the version label.

## Historical records

The following original records are retained in full. Their present-tense wording belongs to the named historical release, not the v0.4 overview.

<details>
<summary>Expand v0.1–v0.3 release-status history</summary>

# Release status

This tree is the public source repository for `v0.3.0-alpha.1`.

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

## v0.2.0-alpha.1 additive status

The v0.2 candidate keeps the same four primary release forms and adds the R39
Smart Router contract delta:

| Form | v0.2 content | Remaining boundary |
|---|---|---|
| Full compatibility distribution | Sanitized R39 host snapshot, frontend, Worker and synchronized core | Exact release gates and target-environment acceptance |
| Certified Bridge Add-on candidate | Price Hook, dispatch/commit/outcome contract and same-artifact validation | No official upstream commit is certified yet |
| Custom Fork Integration Kit | Updated doctor workflow, schemas, vectors and checklist | Every trusted host Hook must be implemented and evidenced |
| Agent Parts Kit | Latest reference core, RoutePrice schema, invariants and fixtures | Development Kit / Not runnable |

Sidecar Lite remains an experimental appendix, not a fifth complete form.

The v0.2 Full reference host contains exact-adapter handling for pseudo-success
HTTP 200 responses, pre-dispatch conversion fallback, side-effecting
single-send behavior and safe-text exhaustive fallback. These host results do
not certify another fork merely because it imports the core.

The release remains a Pre-release. Stable still requires an exact current
upstream Bridge certification, multi-database and lifecycle round trips,
external non-production validation, signed artifacts and complete container
supply-chain evidence.

## v0.3.0-alpha.1 additive status

The v0.3 candidate preserves the four primary forms and adds the R52 contract:

| Form | v0.3 content | Remaining boundary |
|---|---|---|
| Full compatibility distribution | Published v0.2 Full base plus explicit R52 overlay and isolated default/classic builds | Exact reference-host acceptance; no universal fork claim |
| Certified Bridge Add-on candidate | `bridge-spi-v1alpha3`, actual-cost/media/discovery/UI/error observations | Exact host commit and all semantic Hook gates are still uncertified |
| Custom Fork Integration Kit | R52 doctor notes, schemas, vectors and multimodal/error checklist | Host Hooks and database/lifecycle evidence remain required |
| Agent Parts Kit | 20-file synchronized core with cost/cache/media modules and vectors | Development Kit / Not runnable |

The public release documents real cache-rate evidence, output-price-aware ordering,
group colors, exact model mapping, one-Key multimodal discovery and expanded
retry/error handling. These features do not change the host's actual billing
formula. Sidecar Lite remains an experimental appendix and is not a fifth
Full-parity form.

</details>
