## Scope and restraint

- A review SHOULD raise nothing when it finds nothing worth the author's attention. A reviewer
  MUST raise a blocker only for a demonstrable defect, and MUST NOT invent findings to justify
  having reviewed.
- A review MUST be limited to what the change adds or alters. Code the change did not touch
  MUST NOT be criticized, and a reviewer MUST NOT ask for unrelated cleanup, renames or
  restructuring as a condition of proceeding.
- Existing problems that the change merely sits next to MAY be mentioned once as a
  follow-up, and MUST NOT block the change.
- A reviewer MUST judge the change against the problem it states it solves. It MUST NOT
  demand a different or larger design than the one chosen, unless the chosen design cannot
  meet the stated goal, in which case that MUST be raised as a blocker with the reason.
- Preferences about style, naming and structure MUST yield to the project's documented
  standards and to the conventions visible in the surrounding code.
- A reviewer SHOULD say explicitly what it did not review (for example, generated files or
  areas outside its expertise) rather than imply full coverage.
- Findings SHOULD be few and ranked. A long list of low-value points MUST NOT bury the one
  that matters.
