# New API Bridge candidate

This directory defines the minimum trusted in-process boundary required for
Full Smart Router behavior. It is a certification candidate, not a universal
binary plugin.

The Bridge must expose all critical Hooks:

1. post-auth per-Key policy;
2. exact request-contract normalization;
3. exact physical route injection;
4. pre-stream attempt and semantic commit lifecycle;
5. host-transaction quota reserve and settlement;
6. protocol-aware outcome validation;
7. media acceptance, idempotency, task affinity, and reconciliation;
8. role-safe log enrichment and UI entry points.

`Validate` fails closed when a critical Hook is missing. A host integration may
only set `full_parity=true` after the compatibility manifest and conformance
suite pass for an exact New API revision.

The public modules are
`github.com/ginsonko/newapi-smart-router/core` and
`github.com/ginsonko/newapi-smart-router/bridge/newapi`. Pin the exact release
tag or commit and keep the fail-closed compatibility check enabled.
