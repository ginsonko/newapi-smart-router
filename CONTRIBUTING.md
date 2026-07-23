# Contributing

NewAPI Smart Router is derived from and integrates with QuantumNous New API.
Contributions must preserve the upstream AGPL-3.0 license, attribution, NOTICE,
and the additional terms recorded by the upstream project.

Contributions are accepted under AGPL-3.0-only unless a separate written
agreement says otherwise. The commercial-license option covers only material
for which the maintainer has sufficient rights; submitting a contribution does
not silently transfer copyright or relicense upstream code.

## Before changing code

1. State which release form and capability are affected.
2. Identify the exact Route Contract and host Hook boundary.
3. Add or update a minimal failing fixture.
4. Preserve every invariant referenced by `parts/manifest/invariants.yaml`.
5. Run the core tests, conformance checks, package validation, and secret scan.
6. Report capability gaps honestly in the Parity Manifest.

## Changes that require extra review

- authentication or per-Key policy;
- response commit detection and retry;
- billing, reservations, refunds, or reconciliation;
- media submission, idempotency, callbacks, or asset materialization;
- schema migrations and uninstall behavior;
- recovery probes, concurrency, leases, or multi-instance ownership;
- changes to secret scanning, signatures, provenance, or release scripts.

## Prohibited shortcuts

- inferring physical routes from group names;
- treating HTTP 200 as semantic success without contract validation;
- treating a global substring such as `other` as failure;
- replaying after a semantic response has been committed;
- replaying media whose acceptance is unknown;
- counting capacity limits or probes as real-user health failures;
- applying a large patch to an unknown fork without a doctor report;
- claiming parity because the code compiles.

## Test evidence

Pull requests should include commands, toolchain versions, fixture names, and
the resulting Parity Manifest. Production credentials and log bodies must not
be attached.
