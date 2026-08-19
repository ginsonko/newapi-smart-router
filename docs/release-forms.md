# Choosing a release form

## Full compatibility distribution

Choose Full when replacing the application image is acceptable and the site
wants the complete Key UI, planner, recovery worker, billing integration,
media safeguards, logs, and status pages. Use only the exact host revision in
the compatibility manifest. Full is the easiest form for an ordinary site
operator, but it requires the project to keep rebasing and re-certifying New
API updates.

## Certified Bridge Add-on

Choose Bridge when the site stays close to a supported official New API
revision and wants a smaller host diff. The independent core and control plane
remain reusable, but a thin in-process Bridge is still required for post-auth
Key policy, exact channel selection, semantic commit, atomic billing, media
acceptance, logs, and UI entry points. This is plugin-like installation, not a
zero-modification runtime plugin.

## Custom Fork Integration Kit

Choose the Integration Kit for an existing fork with custom authentication,
billing, channel selection, or UI. Run `doctor` first, review missing Hooks,
merge by semantic layer, and prove the result with conformance tests. A build
success is not a parity result.

## Agent Parts Kit

Choose Agent Parts for a deeply modified New API fork, a different gateway, or
an Agent-assisted reimplementation. It supplies algorithms, contracts,
fixtures, and tests. It is deliberately not runnable because a generic package
cannot discover the host's trusted transaction and response-commit boundaries.

## Sidecar Lite

Sidecar Lite remains an experimental appendix. It can provide proxy-level
health, price ordering, and failover, but it cannot guarantee per-Key policy,
actual-route billing, pre-stream commit safety, media idempotency, or native UI
parity without the Bridge Hooks.

## v0.2 additions

All four primary forms now carry the R39 contract delta. Full contains the
reference host implementation; Bridge carries stricter trusted Hook
requirements; Integration carries the doctor/checklist workflow; Agent Parts
carries the synchronized core, RoutePrice schema, invariants and vectors.
Sidecar Lite is unchanged and is not promoted to a fifth full-parity form.

Read [the v0.2 contract delta](v0.2-contract-delta.md) before choosing a form.

## v0.3 additions

The `v0.3.0-alpha.1` update is additive to all four primary forms. It carries
the R52 cache-economy and actual-input-cost evidence, explicit media price
classes, one-Key multimodal discovery, exact model mapping boundaries, group
visual metadata, and the expanded error taxonomy. Full remains a sanitized
reference host; Bridge is `bridge-spi-v1alpha3`; Integration and Parts expose
the same schemas and vectors. None of these additions changes host billing or
turns the Agent Parts Kit into a runnable gateway.
