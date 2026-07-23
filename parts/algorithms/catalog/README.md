# Catalog

Input is a versioned snapshot of enabled channels, abilities, mappings,
groups, adapter capabilities, and administrator blocks. Output is an immutable
catalog keyed by exact contract, not just model name.

- Route identity includes channel generation, group, upstream model, endpoint,
  protocol chain, and capability fingerprint.
- New enabled groups and routes are discovered automatically unless blocked.
- Missing adapter evidence fails closed for that contract.
- A missing price never means free.
- Publication is atomic: readers see the old or new revision, never a partial
  catalog.

