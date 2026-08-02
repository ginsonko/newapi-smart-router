# Conformance

Conformance has three layers:

1. **Golden vectors** compare deterministic core inputs and outputs.
2. **Black-box host tests** call synthetic text and media adapters and observe
   route choice, retry, commit, billing, logs, and failure isolation.
3. **Round-trip and differential tests** prove Bridge-off native behavior,
   database migration, install, rollback, uninstall, and purge.

Run the Go reference vectors from the repository root:

```powershell
go -C core test ./...
go -C core run ./cmd/conformance -vectors testdata/planner-v1.json
```

The v0.2 planner vectors cover real inherited/explicit route prices, mixed
billing-unit isolation, safe-text exhaustion after the historical eighth
attempt, numeric-limit compatibility, side-effecting initial selection,
state-bound pre-routing rejection, and fresh warming evidence.

An alternative-language port must consume the same JSON vectors and match the
same stable Route IDs, rejection reason codes, fixed-point values, and errors.
It cannot replace the host black-box or billing tests.
