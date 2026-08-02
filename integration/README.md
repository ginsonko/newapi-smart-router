# Custom Fork Integration Kit

The Integration Kit is for an existing New API fork. It does not apply a
patch automatically.

1. Run the read-only doctor against a source checkout.
2. Review baseline identity, dirty state, Hook anchors, existing Smart Router
   symbols, and conflicts.
3. Fill `integration-manifest.example.json` with concrete evidence.
4. Integrate by semantic layer: authentication, contract, route, commit and
   outcome, billing, logs, media, then UI.
5. Run core, conformance, Bridge-off differential, database, and round-trip
   tests in an isolated candidate.
6. Promote the Parity Manifest only for the capabilities that actually pass.

Doctor output is evidence for planning. Regex or filename discovery never
certifies Full Parity.

```powershell
python integration/doctor/doctor.py --source C:\path\to\new-api --json report.json
```

The command does not read `.env`, credentials, database files, backups, VCS
objects, logs, or `node_modules`, and it does not modify the checkout.

## v0.2 integration evidence

An R39-equivalent integration additionally proves:

- the price snapshot uses the same resolved global/group-model contract as
  reservation and settlement, with the effective group ratio applied once;
- routes with different price comparison classes are never numerically ordered;
- the disabled attempt-limit mode is mapped only to sequential `safe_text`
  exhaustion and every physical channel is attempted at most once;
- `side_effecting` receives one initial route but no second dispatch, while
  `state_bound` is rejected before upstream connection;
- adapter endpoint declarations narrow rather than broaden the catalog;
- HTTP 200 passes exact semantic validation before commit;
- the serving Web and recovery Worker artifact hashes are identical.

See [the v0.2 contract delta](../docs/v0.2-contract-delta.md) before filling a
host integration or parity manifest.
