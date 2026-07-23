# Algorithm parts

These documents are portable module contracts for Agent-assisted integration.
The authoritative executable reference is `core/smartrouter`; host adapters
must run the same golden vectors before claiming equivalent behavior.

The normative request path is:

```text
authenticate -> exact contract -> hard filters -> strategy rank
  -> cache affinity economics -> capacity decision -> execute
  -> protocol-aware outcome -> bounded retry -> atomic settlement -> explain
```

No later stage may weaken an earlier authorization, price, capability,
context, replay, commit, or billing constraint.

