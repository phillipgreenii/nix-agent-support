## Intent and standards

A reviewer judges a change on two separate axes and SHOULD report them separately.

- **Intent**: Does the change do what it was asked to do, no less and no more? A reviewer
  MUST find the stated goal (the description, the request, the acceptance criteria) before
  judging, and MUST report a gap between the goal and the change, or behavior the change
  adds that nobody asked for.
- **Standards**: Does the change follow the project's own documented standards and the
  conventions of the code around it? A reviewer MUST prefer the project's documented rules
  over its own taste, and MUST NOT report what the project's tooling already enforces.
  Leftover debug output, commented-out code and unexplained placeholders SHOULD be raised
  as nits unless the project's standards forbid them, in which case they are findings.
- **Design**: Where the project documents nothing, a reviewer MAY note a design problem
  (duplicated logic, a name that misleads, an abstraction that leaks, a function doing
  several jobs) but MUST label it as a judgment call and MUST NOT make it a blocker unless
  it will cause a concrete defect.
- If no statement of intent can be found, the reviewer MUST say so and review the standards
  axis and correctness only, rather than guess at the goal.
