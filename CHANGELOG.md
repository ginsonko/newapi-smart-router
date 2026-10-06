# v0.4.0-alpha.1 — Alpha

[Fixed version page](https://github.com/ginsonko/newapi-smart-router/releases/tag/v0.4.0-alpha.1) · [Capabilities and boundaries](docs/v0.4-contract-delta.md). Version notes describe content; publication and CI results are established separately by the version page and build receipts.

- Generic R98 host, authorization/catalog/quote isolation and per-Key nullable policy memory.
- Durable asynchronous image attempts, refund-aware fallback and unknown-outcome reconciliation.
- Standalone core with image health, configurable one-hour default and tier-price expression analysis.
- Reproducible public-base overlay and source/archive privacy validation.
- Historical releases and upstream attribution preserved; no official Bridge certification.

# Changelog

## v0.1.0-alpha.1 - 2026-07-23

Initial public Alpha release:

- four-form release model with machine-checked capability boundaries;
- detailed user, administrator, algorithm, deployment, and troubleshooting
  documentation;
- independently testable Go routing core extracted from the reference fork;
- versioned Route Contract, Route ID, Policy V4, Quality State, Outcome, Route
  Receipt, integration, and parity schemas;
- Bridge Hook contract and fail-closed compatibility candidate;
- read-only Custom Fork doctor and integration manifest workflow;
- Agent Parts Kit with stable invariants, prompts, fixtures, and golden vectors;
- reproducible local packaging, source inventory, SPDX SBOM, checksums, secret
  scanning, and archive round-trip validation.

This version does not claim Stable, rc.21 Bridge compatibility, or
external-site certification.

## v0.2.0-alpha.1 - 2026-08-02

Additive Public Alpha update based on the accepted NewAPI R39 reference:

- synchronized the standalone and Agent Parts Go cores with the current
  15-file `pkg/smartrouter` snapshot;
- added model-aware RoutePrice descriptors and a normative schema;
- aligned inherited and explicit group-model price comparison with actual
  NewAPI billing semantics and removed synthetic `1x` inheritance guidance;
- isolated token, request, second, fixed-duration and expression price classes;
- added default sequential all-eligible fallback for replay-safe text, while
  preserving the optional numeric `1..8` limit;
- admitted side-effecting requests to one initial route while retaining the
  post-dispatch no-second-send fence and state-bound pre-dispatch rejection;
- documented endpoint narrowing, pre-dispatch conversion fallback, false-200
  semantic validation and committed-stream boundaries;
- added fresh warming-evidence re-admission and real-price cache-economy rules;
- added the Web/Worker same-binary Bridge and deployment contract;
- expanded planner conformance from 8 to 16 executable vectors;
- retained the original four adoption forms and kept Sidecar Lite experimental;
- preserved the complete v0.1 README and appended the new manual section.

This version remains Alpha/Pre-release. It does not certify arbitrary NewAPI
forks, universal Bridge compatibility, or Stable supply-chain gates.

## v0.3.0-alpha.1 - 2026-08-19

Additive R52 Smart Router update:

- synchronized the 20-file standalone/Agent Parts core, including actual
  input-cost evidence and media price/route contracts;
- documented real cache-rate evidence, output-price-aware ordering, and the rule that
  ranking never changes billing;
- added one-Key multimodal discovery, exact model mapping boundaries, and
  media acceptance/replay safeguards;
- added UI-only group colors and expanded route/user error taxonomy;
- upgraded the Bridge candidate to `bridge-spi-v1alpha3` and extended doctor,
  manifests, schemas and integration evidence;
- changed the Full builder to use the published v0.2 archive plus an explicit
  R52 overlay, build both frontends in isolation, and remove dependencies
  before packaging;
- retained Alpha/Pre-release and all v0.1/v0.2 README content as an immutable
  prefix.

This release does not claim universal fork compatibility, Stable status, or
production deployment.
