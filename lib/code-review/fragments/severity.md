## Severity bar

Every finding MUST be assigned exactly one of these levels. The level decides whether it is
raised at all.

- **Blocker**: the change, as written, will produce a wrong result, lose or corrupt data,
  open a security hole, break an existing caller, or make the system unsafe to operate. A
  blocker MUST be fixed before the change proceeds. A reviewer MUST be able to state the
  failing scenario in a sentence.
- **Follow-up**: a real problem that is not worth holding this change for, such as a missing
  test for a rare path, a limitation that is documented, or a cleanup the change makes
  newly obvious. A follow-up MUST say what should happen later and MUST be reported as
  a follow-up, not as a blocker.
- **Nit**: a matter of taste or polish that has no effect on behavior. Nits MUST NOT be
  raised when the project's formatter, linter or documented standards already decide the
  matter. A change MUST NOT be held for a nit.
- **Drop**: a suspicion that does not survive verification, or a point that is already
  covered elsewhere in the change or in earlier review. A dropped finding MUST NOT be
  reported.

When unsure between two levels, a reviewer MUST choose the lower one unless it can state
the failing scenario for the higher one. A finding that cannot be verified MUST be dropped
or raised as a question that says what could not be checked, never as a blocker.
