# New API Bridge candidate

This directory defines the minimum trusted in-process boundary required for
Full Smart Router behavior. It is a certification candidate, not a universal
binary plugin.

The Bridge must expose all critical Hooks:

1. post-auth per-Key policy;
2. exact request-contract normalization;
3. exact physical route injection;
4. effective route-price snapshot parity with host billing;
5. pre-stream attempt and semantic commit lifecycle;
6. host-transaction quota reserve and settlement;
7. protocol-aware outcome validation;
8. media acceptance, idempotency, task affinity, and reconciliation;
9. role-safe log enrichment and UI entry points.

`Validate` fails closed when a critical Hook is missing. A host integration may
only set `full_parity=true` after the compatibility manifest and conformance
suite pass for an exact New API revision.

The public modules are
`github.com/ginsonko/newapi-smart-router/core` and
`github.com/ginsonko/newapi-smart-router/bridge/newapi`. Pin the exact release
tag or commit and keep the fail-closed compatibility check enabled.

For v0.2, certification additionally proves effective RoutePrice parity with
billing, safe-text-only exhaustive fallback, the side-effecting post-dispatch
single-send fence, state-bound pre-dispatch rejection, exact adapter semantic
validation, and identical Web/Worker artifact hashes. See
[`docs/v0.2-contract-delta.md`](../../docs/v0.2-contract-delta.md).

## v0.3 additive contract

The `bridge-spi-v1alpha3` candidate carries the R52 additions without changing
the trusted transaction boundary: actual input-cost/cache evidence is a
read-only ranking input, media price and capability contracts are explicit,
and `/v1/models` discovery may expose authorized text, image, video, and audio
models through one Smart Key. Group colors and expanded error explanations are
UI/log metadata only; they never change route identity, authorization, billing,
or retry safety. Unknown host revisions still fail closed, and every host must
prove that its serving Web and recovery Worker artifacts are identical.
