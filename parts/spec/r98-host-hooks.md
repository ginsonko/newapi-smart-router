# R98 trusted host hooks

Bridge protocol `bridge-spi-v1alpha4` retains all earlier Hook obligations and adds:

| Hook | Required behavior | Negative acceptance |
|---|---|---|
| HOOK-POLICY-MEMORY-001 | Persist independent nullable TEXT memory per Key, distinguish absent/empty writes, revalidate owner and active policy, fixed mode clears active only | Failed loading or changing Key identity must never save another Key's draft; an old worker ignores memory |
| HOOK-CATALOG-SCOPE-001 | Authorize catalog, quote, group and exact route against current user scope; refresh after permissions change | Cached UI options do not grant new authorization; quote revision/scope cannot be reused across keys or users |
| HOOK-IMAGE-JOB-001 | Persist root task, intent, upstream acceptance, attempt reservation and terminal outcome; refund once before eligible fallback; configurable real-image health | Timeout or unknown acceptance cannot replay paid dispatch; failed refund cannot advance; fixed-route success can restore shared health |

`Validate` checks host-supplied evidence, not source appearance. A complete fixture in a unit test is not official upstream certification. The compatibility manifest remains uncertified until the exact host revision, runtime artifacts and semantic integration evidence have been independently accepted.

The Full reference integrates these behaviors in token persistence/controller/UI, authorization and pricing/catalog services, image job/task accounting, the scheduler and quality state. Read `docs/v0.4-contract-delta.md` in the repository for migration and feature boundaries.
