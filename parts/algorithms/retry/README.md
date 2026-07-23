# Outcome and retry

Every adapter validates transport, protocol, and business semantics for an
exact contract. HTTP 200 alone is insufficient.

- Empty or malformed success-shaped responses are classified explicitly.
- Valid text, tool calls, structured output, refusals, and media acceptance are
  contract-specific outcomes; no global substring rule is allowed.
- Text may move before the first irreversible semantic output.
- Committed streams never move to another route.
- Media may move only after concrete non-acceptance or with reliable
  idempotency; unknown acceptance stays on the original route for
  reconciliation.
- Retry remains bounded by Key threshold, maximum attempts, unique physical
  channels, deadline, and replay class.

