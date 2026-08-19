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

## v0.2 additive source note

The v0.2 standalone core and Agent Parts reference are mechanically synchronized
from the accepted NewAPI R39 `pkg/smartrouter` snapshot. The release builder
creates a path, size and SHA-256 receipt for the sanitized Full source and binds
that receipt to the public candidate commit. Production hostnames, credentials,
logs, databases, media, deployment receipts and local planning files are not
part of the public source.

## v0.3 additive source note

The v0.3 Full asset starts from the published v0.2 Full archive and applies a
versioned R52 overlay allowlist. The builder records every overlay source,
fixed public-reference blob and assembled digest. Deterministic public
transformations are limited to caller-supplied branding redaction and merging
only translation keys referenced by the selected public UI. It builds the
default and classic frontends in the isolated Full tree using temporary local
dependency links, then removes those links before archiving. No private
site-specific accounting, growth, asset, channel, price, database, log,
deployment, or credential files are eligible inputs. Translation files are
not copied wholesale: the builder merges only keys referenced by the selected
public UI overlay and rejects configured private terms. Four Classic files
excluded or contaminated by the earlier Full assembly are restored byte-for-byte
from the pinned public reference commit recorded in the overlay manifest.
