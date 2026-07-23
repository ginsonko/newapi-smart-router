# Source provenance

The public Alpha is assembled from:

- QuantumNous New API tag `v1.0.0-rc.20`, local HEAD `6ce7305`;
- an audited snapshot of the local Smart Router reference implementation;
- the independently reviewed public documentation and Agent Parts template.

The build records every selected input path and SHA-256 in
`receipts/source-snapshot.json`. It copies source into an independent staging
directory and does not publish the dirty reference Git history.

The 2026-07-23 network audit observed New API `v1.0.0-rc.21` as the latest
release and `main` at `1721144221ec5c94dd87891a7ae1bee228e7bb63`. Neither is
implicitly certified by the rc.20 Bridge candidate. A changed post-auth,
relay, billing, media, or UI Hook requires a fresh compatibility review. The
public repository uses a new sanitized history, retains LICENSE/NOTICE and
third-party notices, and must pass local plus remote secret scanning.
