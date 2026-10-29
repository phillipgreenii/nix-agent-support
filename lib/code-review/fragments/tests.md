## Test skepticism

- A reviewer MUST ask of each new or changed test whether it would fail if the behavior it
  names were broken. A test that cannot fail (it asserts what it just set up, mocks the
  thing under test, or has no assertion) MUST be treated as no test.
- Behavior added or changed by the change SHOULD have a test at the narrowest level that
  proves it. A missing test for a path that could cause a real defect MUST be raised; a missing
  test for a trivial path SHOULD be at most a minor point.
- A test that mocks the dependency whose behavior is the point of the test MUST be called
  out, because it proves nothing about that behavior.
- A test that is slow, order-dependent, time-dependent or reliant on the network SHOULD be
  flagged, and MUST NOT be accepted into a fast, default test run.
- A reviewer MUST NOT accept "tests pass" as evidence of correctness without checking that
  the tests exercise the changed code. A reviewer SHOULD run the relevant tests when it can
  do so without modifying the change.
- Tests that were deleted, skipped, loosened or had their expected values changed in the same
  change as the code they cover MUST be examined first: the change may be hiding a
  regression.
- Where the behavior depends on the semantics of a real tool (a parser, a database, a
  command-line program), a test that runs the real tool SHOULD be preferred, and a test that
  replaces the tool with a stand-in MUST NOT be counted as covering that behavior.
