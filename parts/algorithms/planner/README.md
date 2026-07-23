# Planner

The planner is deterministic for one immutable input snapshot.

1. Enforce user authorization, selected group scope, blacklists, attempt
   history, capabilities, context, price existence, and Key ratio ceiling.
2. Admit routes according to current contract-specific quality evidence.
3. Rank by price, stability, latency, balanced score, or manual order.
4. Evaluate explicit cache affinity without overriding hard constraints.
5. Spill or briefly queue based on capacity without changing health.
6. Return ordered candidates, rejection reason codes, and snapshot versions.

Price-first does not use historical success percentage as a hard admission
threshold. A current successful route can be selected immediately; a later
failure enters bounded retry and shared recovery.

