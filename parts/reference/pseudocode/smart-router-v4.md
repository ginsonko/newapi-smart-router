# Smart Router V4 portable pseudocode

```text
function route(request, key, snapshots, attempt):
  require key.mode == SMART and key.policy.enabled
  require attempt.not_committed and attempt.within_budget

  contract = normalize_exact_contract(request)
  catalog = snapshots.catalog.contract(contract.id)
  candidates = []

  for route in catalog.auto_discovered_routes:
    reject if group not authorized or excluded by key
    reject if route is blocked, already attempted, or same channel attempted
    reject if capability/context/replay contract does not match
    reject if immutable price missing or above key ceiling
    classify current quality for this exact contract
    reject known failed/cooling route except bounded recovery admission
    candidates.append(route with price, quality, ttft, capacity, admission)

  stable_sort candidates by key.strategy and canonical tie-breaks
  candidates = evaluate_cache_economy_after_hard_constraints(candidates)
  choice = first immediate-capacity route or justified bounded queue
  receipt = reserve_atomically(choice and all snapshot revisions)

  outcome = execute_and_validate(choice, contract, commit_guard)
  update separate real/probe/credential/capacity evidence

  if outcome.retry_allowed and replay_class_allows and attempt.within_budget:
    retry with next unique physical channel using the same frozen policy/price
  else:
    settle_or_reconcile_atomically(receipt, outcome)
```

The host integration must provide the exact contract normalizer, semantic
commit guard, protocol-aware outcome validator, atomic quota transaction, and
media acceptance/idempotency state. Omitting one is a capability gap, not an
implementation detail.

