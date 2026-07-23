# Cache economy

Cache affinity is an economic optimization after hard filtering and ranking.

The fixed mode preserves a healthy route only within a user-visible premium.
The break-even mode compares expected cache rebuild loss against discounted
future route savings using bounded, privacy-preserving evidence.

- `affinity_ttl_seconds` expires evidence; it is not a route lock.
- A hard failure, capacity block, price ceiling, authorization change, or
  clearly beneficial switch can move immediately.
- Cache namespaces include credential/generation/protocol revision.
- Low confidence or drift falls back to the fixed, auditable rule.
- Raw prompts, User-Agent strings, and cache keys are not retained.

