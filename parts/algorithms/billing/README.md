# Billing

Route choice and billing meet at an immutable Route Receipt.

- Freeze contract, physical route, real group, effective ratio, catalog/price/
  policy/health revisions, attempt index, and reservation identity.
- Reserve, increase, release, refund, and settle idempotently.
- Charge the actual successful physical route, not the requested virtual pool.
- Do not replay an upstream success because a local transaction failed.
- Put uncertain committed outcomes into reconciliation with a durable receipt.
- Keep host quota changes and receipt settlement in the same trusted database
  transaction.

