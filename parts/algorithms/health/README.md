# Health

Quality is scoped to the exact Route Contract. Evidence from an Anthropic
Messages path cannot make an OpenAI Responses path healthy or unhealthy.

- Real request evidence and probe evidence are stored separately.
- Infrastructure and protocol failures affect route health.
- Credential failures affect a credential domain.
- 429 and concurrency affect capacity, not semantic health.
- User input, content rejection, client cancellation, and host-local errors do
  not poison shared route health.
- A fresh real or probe success is current availability evidence; price-first
  may use it without an extra fixed recovery cooldown.

