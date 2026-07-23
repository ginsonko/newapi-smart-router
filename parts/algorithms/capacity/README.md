# Capacity

Capacity is a separate state domain keyed by the upstream concurrency pool.

- Acquire an inflight lease immediately when possible.
- For a saturated cheap route, optionally wait only within the Key's short-wait
  budget, request deadline, and minimum-saving threshold.
- Otherwise spill to the next eligible route.
- Release leases on success, failure, cancellation, and panic paths.
- Honor Retry-After without marking the route semantically unhealthy.

