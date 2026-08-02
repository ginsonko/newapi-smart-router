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
