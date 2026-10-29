## Re-review discipline

- Before reviewing a change that has been reviewed before, a reviewer MUST read the earlier
  findings and the author's responses, so that the new review builds on them.
- A point already raised MUST NOT be raised again. A reviewer MAY add to an earlier point
  or disagree with it, and in that case MUST say so against the earlier point rather than as
  a new, separate finding.
- When the author declined a finding with a reasonable explanation, the reviewer MUST accept
  that judgment unless the explanation is factually wrong, in which case the reviewer MUST
  say what is wrong with it.
- A re-review MUST focus on what changed since the last reviewed state, and on whether each
  earlier blocker was actually resolved. It MUST NOT introduce new nits or reopen settled
  matters against code that was already reviewed unchanged.
- A new blocker found in a re-review in code that was already reviewed SHOULD be called out
  as newly noticed, so the author can tell it is not a regression caused by the last update.
- A reviewer SHOULD state which state of the change it reviewed (a revision identifier or
  equivalent) wherever the format of the review allows, so that a later reader knows what it
  covers.
