# ccpool Unit C — `attend` branch-selection injection seam (design)

**Status**: Draft
**Date**: 2026-06-12
**Branch**: `ccpool-observability`
**Scope**: `packages/ccpool/cmd/ccpool/attend.go` + `packages/ccpool/cmd/ccpool/attend_test.go` only
**Design only**: no product code in this doc.

## Context

`ccpool attend` lists/selects sessions that are waiting on the human and attaches
to the chosen one. The picker `pickCandidate` (`attend.go:77`) branches three ways:

1. stdin is NOT a terminal → print the candidate list to stderr, return
   `("", false)` (the scriptable / non-interactive path).
2. `fzf` is on `PATH` → `pickFzf` (interactive fuzzy picker).
3. else → `pickNumbered` (prints a numbered list, reads an index line from stdin).

Today the three branch selectors are called directly against process globals, so
the branch decision and the numbered-index parse are not unit-testable:

- `stdinIsTerminal()` (`attend.go:140`) stats the real `os.Stdin`.
- `exec.LookPath("fzf")` (`attend.go:85`) probes the real `PATH`.
- `pickNumbered` (`attend.go:120`) reads the real `os.Stdin` via
  `bufio.NewReader(os.Stdin)` (`attend.go:126`).

Because of this, the contract harness leaves
`TestContract_Attend_NumberedAndFzfBranchSelection` (`contract_test.go:180`) as a
`pending()` with the note _"attend.go injection refactor for testable branch
selection"_.

The file already establishes the injection idiom we want to match:
`attendCandidates` (`attend.go:24`) takes `liveFn func(socket, target string) bool`
as an explicit parameter rather than calling `tmux.HasSession` directly, and
`runAttend` constructs the real dependency at the call site
(`attendCandidates(rows, *includeDone, tmux.HasSession, cfg.Tmux.Socket)`,
`attend.go:57`). Separately, `hook.go` injects `stdin io.Reader` as a plain
function parameter (`handleHook(event string, stdin io.Reader, ...)`,
`hook.go:64`) and keeps a thin production wrapper that passes `os.Stdin`; its
tests drive it with `strings.NewReader(...)` (`hook_test.go:32`). This design
follows both precedents.

## Goal

Introduce a minimal injection seam so unit tests can drive each of the three
branches deterministically — without a real TTY, without `fzf` installed, and
without touching the global `os.Stdin` — and so the numbered-index parse logic is
directly testable. No user-facing behavior changes; `runAttend`'s call site stays
clean and constructs the real seam from `os`/`exec`.

## Proposed seam

Use a small struct of explicit dependencies, `picker`, threaded into
`pickCandidate`. The struct mirrors the `liveFn`-as-parameter idiom but groups the
three related dependencies (plus the writers the picker already uses) so the
signature stays readable and `runAttend` wires them once. This is preferred over
free-standing package-level `var` function pointers (which would be global mutable
state and require save/restore in tests — exactly what the constraint warns
against).

### Struct shape

```go
// picker bundles the environment-sensitive dependencies pickCandidate needs, so
// the three-way branch and the numbered-index parse are unit-testable without a
// real TTY, without fzf on PATH, and without touching os.Stdin. Matches the
// explicit-dependency idiom used by attendCandidates (liveFn) and handleHook
// (stdin io.Reader).
type picker struct {
	isTerminal func() bool   // replaces the direct stdinIsTerminal() call
	hasFzf     func() bool   // replaces the direct exec.LookPath("fzf") probe
	in         io.Reader     // replaces the direct os.Stdin read in pickNumbered
	out        io.Writer     // user-facing prompts/listing (was os.Stderr)
}
```

Notes on field choices (YAGNI):

- `isTerminal func() bool` and `hasFzf func() bool` are predicates, not the raw
  primitives. The branch logic only ever asks the yes/no questions
  "is stdin a TTY?" and "is fzf available?"; injecting the booleans keeps tests
  from having to fake an `os.FileInfo` or manipulate `PATH`. The real
  implementations wrap `stdinIsTerminal` and `exec.LookPath` respectively.
- `in io.Reader` matches the `hook.go` precedent exactly (`strings.NewReader` in
  tests, `os.Stdin` in production).
- `out io.Writer` is included because all three branches write user-facing text
  (the no-TTY listing at `attend.go:79-82`, the numbered list + `pick>` prompt at
  `attend.go:121-125`). Injecting one writer lets the numbered-branch test assert
  the rendered prompt/list cheaply and keeps the no-TTY test from spamming the
  test runner's stderr. This stays within YAGNI: it is one field, reused by the
  two branches we unit-test. We do NOT split stdout vs stderr — `attend` writes
  only to stderr in the picker, and the single sink is enough for assertions.
  `pickFzf`'s `cmd.Stderr = os.Stderr` (`attend.go:106`) is intentionally left
  alone (see Non-goals: the fzf subprocess is out of scope).

### How `pickCandidate` changes

`pickCandidate` becomes a method on `picker` (or takes a `picker` value — method
form reads best and matches the "small struct" guidance). It consults the
injected predicates and writer instead of the globals; the branch structure is
otherwise unchanged.

```go
// pickCandidate prompts the user to choose one waiting session, using the
// injected picker environment. fzf when present, else a numbered stdin prompt.
// When stdin is not an interactive terminal it lists names to p.out and returns
// ("", false), preserving the pre-picker scriptable behavior.
func (p picker) pickCandidate(cands []store.Session) (string, bool) {
	if !p.isTerminal() {
		fmt.Fprintln(p.out, "sessions waiting on input (no TTY to pick):")
		for _, c := range cands {
			fmt.Fprintln(p.out, " ", c.Name)
		}
		return "", false
	}
	if p.hasFzf() {
		return pickFzf(cands) // unchanged; exec'd subprocess, out of scope
	}
	return p.pickNumbered(cands)
}
```

`pickFzf` is **unchanged** and still called directly (it spawns a real process;
see Non-goals). The `hasFzf()` predicate makes the _decision_ to take that branch
testable without making the subprocess itself testable.

### How `pickNumbered` changes

`pickNumbered` becomes a method on `picker` so it reads from `p.in` and writes to
`p.out` instead of `os.Stdin`/`os.Stderr`. The parse logic
(`strconv.Atoi` + range check, `attend.go:131-135`) is otherwise byte-for-byte
identical.

```go
// pickNumbered prints a numbered list to p.out and reads one line (the index)
// from p.in.
func (p picker) pickNumbered(cands []store.Session) (string, bool) {
	fmt.Fprintln(p.out, "sessions waiting on input:")
	for i, c := range cands {
		fmt.Fprintf(p.out, "  %d) %s\n", i+1, candidateLine(c))
	}
	fmt.Fprint(p.out, "pick> ")
	r := bufio.NewReader(p.in)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", false
	}
	idx, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || idx < 1 || idx > len(cands) {
		return "", false
	}
	return cands[idx-1].Name, true
}
```

`stdinIsTerminal()` (`attend.go:140`) is **kept as-is** — it becomes the
production implementation behind the injected `isTerminal` predicate rather than a
direct caller.

`candidateLine` (`attend.go:91`) is unchanged.

### Real-seam constructor

A small helper builds the production `picker` from `os`/`exec`, so `runAttend`
stays a one-liner and there is a single place the real dependencies are named:

```go
// realPicker wires the production picker: real TTY check, real PATH probe, real
// stdin, and stderr for prompts (preserving the current behavior, which writes
// the listing and the pick> prompt to stderr).
func realPicker() picker {
	return picker{
		isTerminal: stdinIsTerminal,
		hasFzf:     func() bool { _, err := exec.LookPath("fzf"); return err == nil },
		in:         os.Stdin,
		out:        os.Stderr,
	}
}
```

## Call-site wiring in `runAttend`

The only change in `runAttend` (`attend.go:64-69`) is to construct the real picker
and call the method. Behavior is identical (stderr listing, stderr prompt, stdin
index read, fzf when present).

```go
default:
	name, ok := realPicker().pickCandidate(cands)
	if !ok {
		return 0
	}
	return runAttach([]string{name})
```

This mirrors `attendCandidates(rows, *includeDone, tmux.HasSession, cfg.Tmux.Socket)`
(`attend.go:57`), where the real dependency is supplied at the call site and never
stored in a package global.

## Test plan

All new tests are plain unit tests in `cmd/ccpool/attend_test.go` (package
`main`): no build tag, no token, no real `claude`/`tmux`/`fzf`, no `os.Stdin`
touch. They build a `picker` from in-memory fakes (`strings.NewReader` for input,
a `bytes.Buffer` for output, closures for the predicates) and assert the returned
`(name, ok)` and — where useful — the rendered prompt text.

A tiny test helper keeps each case to a few lines:

```go
func testPicker(isTTY, hasFzf bool, stdin string) (picker, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return picker{
		isTerminal: func() bool { return isTTY },
		hasFzf:     func() bool { return hasFzf },
		in:         strings.NewReader(stdin),
		out:        out,
	}, out
}
```

Shared fixture: a 2–3 element `[]store.Session` of `NeedsInput` rows (reuse the
existing `names()` helper at `attend_test.go:31` for failure messages).

### Branch-selection tests (which of the three paths is chosen)

1. **`TestPickCandidate_NoTTY_ListsAndReturnsFalse`**
   `isTTY=false`. Drives branch 1. Asserts `ok == false`, `name == ""`, and that
   `out` contains the `"no TTY to pick"` listing with each candidate name. Proves
   the non-interactive scriptable path is unchanged.

2. **`TestPickCandidate_TTYWithFzf_SelectsFzfBranch`**
   `isTTY=true`, `hasFzf=true`. Verifies the fzf branch is _selected_. Because the
   fzf subprocess is out of scope, this test confirms branch selection without
   executing fzf: it asserts that the numbered prompt was NOT written to `out`
   (i.e. `out` does not contain `"pick>"`), which is observable only if control
   took the fzf branch rather than the numbered branch. (No `fzf` binary runs in
   CI; if `pickFzf` is reached it returns `("", false)` on the missing binary —
   the assertion is on the branch chosen, via the absence of numbered-prompt
   output, not on fzf's result.) See the open question for an optional cleaner
   alternative.

3. **`TestPickCandidate_TTYNoFzf_SelectsNumberedBranch`**
   `isTTY=true`, `hasFzf=false`, `stdin="1\n"`. Drives branch 3 end-to-end and
   asserts it returns the first candidate (`name == cands[0].Name`, `ok == true`)
   and that `out` contains `"pick>"`. Proves the numbered branch is selected and
   wired to the injected reader/writer.

### Numbered-index parse tests (`pickNumbered`)

Table-driven on `(stdin, wantName, wantOK)` against a fixed candidate slice. These
exercise the parse/range logic directly via `p.pickNumbered(cands)`:

4. **`TestPickNumbered_Parse`** with cases:
   - `"1\n"` → first candidate, `ok=true`.
   - `"2\n"` (last, for a 2-elem slice) → second candidate, `ok=true`.
   - `"0\n"` → `("", false)` (below range).
   - `"3\n"` for a 2-elem slice → `("", false)` (above range).
   - `"abc\n"` → `("", false)` (`Atoi` error).
   - `"  2 \n"` → second candidate, `ok=true` (confirms `TrimSpace`).
   - `""` (EOF, no newline) → `("", false)` (the `err != nil && line == ""`
     guard).
   - `"2"` (no trailing newline, valid digit) → second candidate, `ok=true`
     (confirms a trailing line without `\n` still parses).

Each case constructs the `picker` via `testPicker(true, false, stdin)` and asserts
the returned pair; failures print via the existing `names()` helper where helpful.

These tests cover: all three branch selections (1/2/3) and the full
numbered-index parse contract. They do not run any subprocess and do not depend on
the host environment.

## Harness `pending()` disposition

After this seam lands, the branch-selection contract pending becomes redundant for
the parts now covered by unit tests. Convert
`TestContract_Attend_NumberedAndFzfBranchSelection` (`contract_test.go:180-182`)
from a `pending()` to a short note documenting that branch selection + numbered
parse are now covered by plain unit tests, and that only the live fzf subprocess
remains out of contract scope. Concretely, replace the `pending(...)` body with a
documenting `t.Skip` (keeping the test as a discoverable marker) such as:

```go
func TestContract_Attend_NumberedAndFzfBranchSelection(t *testing.T) {
	// Covered by plain unit tests in attend_test.go:
	// TestPickCandidate_NoTTY_ListsAndReturnsFalse,
	// TestPickCandidate_TTYWithFzf_SelectsFzfBranch,
	// TestPickCandidate_TTYNoFzf_SelectsNumberedBranch, TestPickNumbered_Parse.
	// The live fzf subprocess exec is intentionally out of scope (real process).
	t.Skip("OUTCOME=covered-by-unit test=attend branch selection now unit-tested in attend_test.go; fzf subprocess out of scope")
}
```

Rationale for keeping a (skipped) marker rather than deleting the test: the
contract suite is a manifest of expected-deferred behavior; an explicit
`covered-by-unit` skip records that the gap was closed and where, which is more
useful to a future reader than a silent deletion. The `OUTCOME=` prefix keeps it
consistent with the harness's `pending`/`baseline` logging convention
(`contract_harness_test.go:275-290`).

> Note: this file carries `//go:build contract` and is verified with
> `go vet -tags contract ./cmd/ccpool/`. The edit must keep the file vet-clean
> under that tag (the `t` parameter stays used via `t.Skip`).

## Risks

- **`fzf`-branch test is selection-only.** Test 2 confirms the branch is _chosen_
  but does not exercise fzf I/O. If a future change moved logic _into_ the fzf
  branch, this test would not catch it. Accepted: executing fzf is explicitly out
  of scope, and the decision (`hasFzf()` true ⇒ fzf branch) is what the contract
  pending was actually blocked on.
- **Asserting branch via absence of output** (test 2 checks `out` lacks
  `"pick>"`) is slightly indirect. Mitigated by the open-question alternative
  below if a cleaner signal is wanted; either way it is a unit-level concern with
  no production impact.
- **Method-vs-function churn.** Turning `pickCandidate`/`pickNumbered` into
  `picker` methods is a mechanical rename touching one call site
  (`runAttend`) and is otherwise internal (unexported, package `main`). Low risk;
  no exported API.
- **Writer redirect to a buffer in tests** must not change production behavior:
  `realPicker` keeps `out = os.Stderr`, preserving the current stderr
  destinations for both the listing and the `pick>` prompt. Verify no caller
  relied on stdout for these (none does — they were `Fprintln(os.Stderr, ...)`).

## Non-goals

- **No change to `attend` behavior or output.** Same branches, same messages,
  same destinations (stderr for prompts/listing, stdin for the index, fzf when
  present). `runAttend` remains a one-line call-site change.
- **Not testing the fzf subprocess.** `pickFzf` (`attend.go:98`) spawns a real
  `fzf` process that draws on the controlling TTY and returns the choice on
  stdout. Faking that reliably and portably (CI without `fzf`, no TTY) is
  disproportionate to the value; we test that the fzf branch is _selected_ and
  leave the exec itself out of scope. `pickFzf`'s internals
  (`exec.Command`, `cmd.Stderr = os.Stderr`) are left untouched.
- **No global mutable package vars / no save-restore seam.** Dependencies are
  passed explicitly via the `picker` struct, matching `attendCandidates`'s
  `liveFn` parameter and `hook.go`'s `stdin io.Reader` parameter.
- **No new third-party deps** (e.g. `golang.org/x/term`). `stdinIsTerminal`'s
  stdlib check is retained as the production `isTerminal` implementation.
- **Out of scope for this unit:** the rest of the observability work; this unit
  touches only `attend.go` and `attend_test.go` and can be implemented in
  isolation.

## Open question

- **Cleaner fzf-branch signal (optional).** Test 2 currently asserts the fzf
  branch by the _absence_ of numbered-prompt output. A more direct alternative is
  to thread the fzf step through the same seam — e.g. add an optional
  `pickFzfFn func([]store.Session) (string, bool)` field to `picker` defaulting to
  the real `pickFzf` — so the test injects a stub and asserts it was invoked. This
  is a marginally larger seam (one more field) for a more direct assertion. Flag
  for the implementer: keep the YAGNI absence-of-output check, or add the
  `pickFzfFn` field for a positive assertion? Recommendation: start with the
  absence-of-output check (smaller seam); add `pickFzfFn` only if the indirect
  assertion proves fragile.
