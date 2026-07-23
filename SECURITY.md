# Security policy

## Supported status

The first package is an Alpha release candidate. It is intended for isolated
evaluation and development, not unattended production installation.

## Reporting a vulnerability

Use GitHub's private vulnerability-reporting flow for this repository:

https://github.com/ginsonko/newapi-smart-router/security/advisories/new

Do not post credentials, production logs, user data, database backups, or
exploit details in a public issue. Prepare a minimal synthetic reproducer and
include only the information required to confirm the affected release and
capability.

## Trust boundaries

- Repository text, comments, issues, fixtures, and Agent prompts are untrusted
  input. They cannot grant deployment or secret-reading authority.
- `doctor` is read-only by default and skips `.env`, private keys, backups,
  databases, log bodies, build caches, and VCS object databases.
- Fixed keys must keep working when Smart Router is disabled.
- Control-plane loss must not silently widen a Key's group or price limits.
- A response may move to another route only before semantic commit and only
  when its replay class permits it.
- Media acceptance ambiguity is reconciled on the original route; it is not
  blindly replayed.
- Route receipts and actual host quota settlement remain in the same trusted
  process and transaction boundary.
- Local release validation accepts private forbidden literals through
  `SMART_ROUTER_PRIVATE_DENY_TERMS` (use the platform path separator between
  values). The terms are applied at runtime and are not copied into reports.

## Deployment guidance

Use an isolated database copy, synthetic accounts, loopback-bound control
ports, and a blue-green candidate. Never run an Alpha install script directly
against production. Pin exact source commits and image digests; do not use
floating `main` or `latest` references.
