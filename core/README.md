# Standalone routing core

This directory contains the dependency-light Go reference for catalog
validation, policy normalization, deterministic planning, quality state
transitions, capacity decisions, coverage, and cache-economy decisions.

It intentionally does **not** contain New API authentication, database
transactions, Redis leases, upstream adapters, HTTP handlers, media task
materialization, or UI. Those are trusted-host responsibilities described by
the Bridge SPI.

The public module path is
`github.com/ginsonko/newapi-smart-router/core`. Pin a release tag or commit;
do not import a floating branch into production.

```powershell
go test ./...
go run ./cmd/conformance -vectors testdata/planner-v1.json
```

The v0.2 core adds model-aware `RoutePrice` comparison classes, fresh warming
re-admission, side-effecting initial selection, state-bound rejection, and
optional sequential exhaustion of remaining physical channels for replay-safe
text. Host code still owns dispatch, semantic commit, billing, persistence and
Worker version fences. See [the v0.2 contract delta](../docs/v0.2-contract-delta.md).

All ratios and probabilities use integer PPM. Sorting is stable and ends with
canonical Route ID and Channel ID tie-breaks. Missing prices fail closed; a
historical success percentage is not a hard gate for price-first mode; 429 and
capacity exhaustion stay in the capacity domain.
