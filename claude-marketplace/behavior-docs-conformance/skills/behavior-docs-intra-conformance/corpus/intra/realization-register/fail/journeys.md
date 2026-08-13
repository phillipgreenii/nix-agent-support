# Use cases & open questions — sample (realization-register FAIL fixture, INV-23)

## Open questions

- **`OQ-1` — Realization tracked externally, not annotated inline.** These docs state intended
  behavior only (`INV-2`); a **realization gap** — intended behavior the implementation has not yet
  built — is normal and is tracked against the cited ID (`INV-15`). Rules whose realization is worth
  tracking this way today: `INV-1`. _Owner_: author. _Path_: track realization against these IDs.

INTRA expectation: FLAG on `realization-register` — twice over. The set carries no
`## Realization gaps` section, and the gaps it does record live inside an `OQ-` element. An open
question is **unsettled intent**; a realization gap is **settled intent the build has not reached**,
so this quotes `INV-2`/`INV-15` while breaking both — it puts implementation-status prose inside an
element definition and mints a citable identity (`INV-3`) for a record that must be deleted when the
gap closes. This is the real-world shape the rule was written against (`INV-23`).
