# Recovery

Recovery runs off the ordinary user request path whenever possible.

- Failed routes enter a durable due queue with lease, jitter, global and
  failure-domain budgets.
- The first outage hour may probe at the configured fast interval; persistent
  outages use bounded slower backoff but remain recoverable unless blocked.
- Successful probes stop paid synthetic probing and move the route into a
  warming/currently-available state.
- Unknown cold-start routes are explored in bounded waves while the user can
  still fall through to known routes.
- Recovery jobs survive worker restart and converge across multiple instances.

