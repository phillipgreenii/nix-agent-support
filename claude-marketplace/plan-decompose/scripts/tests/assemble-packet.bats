#!/usr/bin/env bats
# bats file_tags=type:unit
#
# assemble-packet.sh against the REAL script and a throwaway git fixture
# (speedup plan P4; bead pg2-ltvod). Covers the plan's P4 test list (single
# section, repeated heading resolved by path, ambiguous -> 4, not found -> 3,
# heading inside a code fence ignored, last section in file, table row with
# header, @@file, --quote, diff-hunk pass-through, no re-expansion, byte
# identity of non-directive lines) plus exit-code 2/1 cases and the @@file
# read boundary (outside the git toplevel by absolute path, by `..`, through a
# symlink, and into .git, all refused with exit 3).

# GFH_LIB is the one required file (tc-4ehow): no filesystem search.
if [[ -z ${GFH_LIB:-} ]]; then
  echo "GFH_LIB is not set: commit via the run-unit-tests hook, run under the workspace .envrc (direnv, or direnv exec <workspace-root> ...), or export GFH_LIB=<path to git-fixture-harness.bash>." >&2
  exit 1
fi
GFH_HARNESS_FILE="$GFH_LIB"
# shellcheck disable=SC1090
source "$GFH_HARNESS_FILE"

SCRIPT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)/assemble-packet.sh"
SUITE="plan-decompose-assemble-packet"

setup() {
  gfh_setup "$SUITE"
  REPO="$GFH_REPO"
  OUTSIDE="$GFH_WORK/outside"
  mkdir -p "$OUTSIDE" "$REPO/scratch"
  DESIGN="$REPO/scratch/design.md"
  DRAFT="$REPO/scratch/draft.md"
  BODY="$REPO/scratch/body.md"
  cd "$REPO" || return 1
}

teardown() {
  gfh_teardown
}

# Writes the shared design fixture.
write_design() {
  cat >"$DESIGN" <<'EOF'
# Plan

Intro text.

## Phase 1

Phase one overview.

### Validation

Phase one validation line A.
Phase one validation line B.

### Notes

Phase one notes.

## Phase 2

### Validation

Phase two validation text.

## Binding decisions

First decision line.

```text
# not a heading, inside a fence
@@file /etc/passwd
```

Last decision line.

## Table section

| Name | Value |
| ---- | ----- |
| alpha | one |
| beta | two |
| alpha | three |

After the table.

## Final

The last section, no trailing newline.
EOF
  # Drop the final newline so the last section ends at EOF without one.
  printf '%s' "$(cat "$DESIGN")" >"$DESIGN.tmp"
  mv "$DESIGN.tmp" "$DESIGN"
}

assemble() {
  # --design is passed only when the fixture exists, so @@file-only tests need no design.
  if [[ -f $DESIGN ]]; then
    run "$SCRIPT" --design "$DESIGN" --in "$DRAFT" --out "$BODY"
  else
    run "$SCRIPT" --in "$DRAFT" --out "$BODY"
  fi
}

# ---------------------------------------------------------------- help / usage

@test "-h and --help print usage and exit 0" {
  run "$SCRIPT" -h
  [ "$status" -eq 0 ]
  [[ $output == *"assemble-packet.sh: expand exact-excerpt directives"* ]]
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ $output == *"@@design-table-row"* ]]
}

@test "missing --in, missing --out, unknown flag and stray argument exit 2" {
  write_design
  printf 'x\n' >"$DRAFT"
  run "$SCRIPT" --design "$DESIGN" --out "$BODY"
  [ "$status" -eq 2 ]
  run "$SCRIPT" --design "$DESIGN" --in "$DRAFT"
  [ "$status" -eq 2 ]
  run "$SCRIPT" --bogus
  [ "$status" -eq 2 ]
  run "$SCRIPT" stray
  [ "$status" -eq 2 ]
  run "$SCRIPT" --design
  [ "$status" -eq 2 ]
}

@test "--out equal to --in exits 2 and leaves the draft untouched" {
  printf 'keep me\n' >"$DRAFT"
  run "$SCRIPT" --in "$DRAFT" --out "$DRAFT"
  [ "$status" -eq 2 ]
  [ "$(cat "$DRAFT")" = "keep me" ]
}

@test "unreadable draft or design exits 1" {
  run "$SCRIPT" --in "$REPO/scratch/missing.md" --out "$BODY"
  [ "$status" -eq 1 ]
  printf 'x\n' >"$DRAFT"
  run "$SCRIPT" --design "$REPO/scratch/missing.md" --in "$DRAFT" --out "$BODY"
  [ "$status" -eq 1 ]
}

@test "a directive with an empty argument exits 2" {
  write_design
  printf '@@design-section \n' >"$DRAFT"
  assemble
  [ "$status" -eq 2 ]
  printf '@@design-section --quote\n' >"$DRAFT"
  assemble
  [ "$status" -eq 2 ]
  printf '@@file \n' >"$DRAFT"
  assemble
  [ "$status" -eq 2 ]
  printf '@@design-table-row Table section\n' >"$DRAFT"
  assemble
  [ "$status" -eq 2 ]
  printf '@@design-section --bogus Final\n' >"$DRAFT"
  assemble
  [ "$status" -eq 2 ]
  [ ! -e "$BODY" ]
}

@test "a design directive without --design exits 2" {
  printf '@@design-section Final\n' >"$DRAFT"
  run "$SCRIPT" --in "$DRAFT" --out "$BODY"
  [ "$status" -eq 2 ]
}

# ------------------------------------------------------------- design sections

@test "single section: heading omitted, text verbatim, citation marker last" {
  write_design
  printf 'before\n@@design-section Phase 2\nafter\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  # Phase 2 owns no text of its own: the next heading (any level) ends it.
  [ "$(cat "$BODY")" = "$(printf 'before\n[design: Phase 2]\nafter')" ]
}

@test "a section runs to the next heading of ANY level" {
  write_design
  printf '@@design-section Phase 1\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  [ "$(cat "$BODY")" = "$(printf 'Phase one overview.\n[design: Phase 1]')" ]
}

@test "a repeated heading is resolved by Parent > Child, marker keeps the path as given" {
  write_design
  printf '@@design-section Phase 2 > Validation\n@@design-section Phase 1 > Validation\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  expected=$(printf 'Phase two validation text.\n[design: Phase 2 > Validation]\nPhase one validation line A.\nPhase one validation line B.\n[design: Phase 1 > Validation]')
  [ "$(cat "$BODY")" = "$expected" ]
}

@test "a Parent > Child path may skip intermediate levels" {
  write_design
  printf '@@design-section Plan > Notes\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  [ "$(cat "$BODY")" = "$(printf 'Phase one notes.\n[design: Plan > Notes]')" ]
}

@test "an ambiguous heading exits 4 and lists the disambiguating parent paths" {
  write_design
  printf '@@design-section Validation\n' >"$DRAFT"
  assemble
  [ "$status" -eq 4 ]
  [[ $output == *"ambiguous"* ]]
  [[ $output == *"Plan > Phase 1 > Validation"* ]]
  [[ $output == *"Plan > Phase 2 > Validation"* ]]
  [ ! -e "$BODY" ]
}

@test "a missing heading exits 3 and writes no output" {
  write_design
  printf '@@design-section No Such Heading\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  [[ $output == *"heading not found"* ]]
  [ ! -e "$BODY" ]
}

@test "a wrong parent path exits 3" {
  write_design
  printf '@@design-section Phase 2 > Notes\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
}

@test "a failing directive leaves a pre-existing --out untouched and no temp file" {
  write_design
  printf 'previous\n' >"$BODY"
  printf 'line\n@@design-section No Such Heading\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  [ "$(cat "$BODY")" = "previous" ]
  run bash -c 'ls -A "$1"' _ "$REPO/scratch"
  [[ $output != *".assemble-packet."* ]]
}

@test "a heading inside a code fence is ignored, and fence lines do not end a section" {
  write_design
  printf '@@design-section Binding decisions\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  expected=$(printf 'First decision line.\n\n```text\n# not a heading, inside a fence\n@@file /etc/passwd\n```\n\nLast decision line.\n[design: Binding decisions]')
  [ "$(cat "$BODY")" = "$expected" ]
  printf '@@design-section not a heading, inside a fence\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
}

@test "the last section in the file works even without a trailing newline" {
  write_design
  printf '@@design-section Final\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  [ "$(cat "$BODY")" = "$(printf 'The last section, no trailing newline.\n[design: Final]')" ]
}

@test "--quote prefixes every emitted line with '> '" {
  write_design
  printf '@@design-section --quote Phase 1 > Validation\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  expected=$(printf '> Phase one validation line A.\n> Phase one validation line B.\n> [design: Phase 1 > Validation]')
  [ "$(cat "$BODY")" = "$expected" ]
}

@test "--quote on a section with a blank line emits '> ' for it" {
  write_design
  printf '@@design-section --quote Binding decisions\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  # Every line, blank ones included, carries the prefix.
  run grep -c -v '^> ' "$BODY"
  [ "$output" = "0" ]
}

@test "ATX heading trailing hashes and CRLF design lines are tolerated in matching" {
  printf '# Top ##\r\nbody one\r\n' >"$DESIGN"
  printf '@@design-section Top\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  [ "$(cat "$BODY")" = "$(printf 'body one\r\n[design: Top]')" ]
}

# ------------------------------------------------------------------ table rows

@test "table row emits header, separator and the matching rows, then the marker" {
  write_design
  printf '@@design-table-row Table section | beta\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  expected=$(printf '| Name | Value |\n| ---- | ----- |\n| beta | two |\n[design: Table section]')
  [ "$(cat "$BODY")" = "$expected" ]
}

@test "table row emits every row whose first cell matches" {
  write_design
  printf '@@design-table-row Table section | alpha\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  expected=$(printf '| Name | Value |\n| ---- | ----- |\n| alpha | one |\n| alpha | three |\n[design: Table section]')
  [ "$(cat "$BODY")" = "$expected" ]
}

@test "table row: unknown row, no table, unknown heading exit 3; ambiguous heading exits 4" {
  write_design
  printf '@@design-table-row Table section | gamma\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  printf '@@design-table-row Final | x\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  printf '@@design-table-row Nowhere | x\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  printf '@@design-table-row Validation | x\n' >"$DRAFT"
  assemble
  [ "$status" -eq 4 ]
  [ ! -e "$BODY" ]
}

@test "table row ignores a table-looking block inside a code fence" {
  printf '## T\n\n```\n| a | b |\n| - | - |\n| x | y |\n```\n' >"$DESIGN"
  printf '@@design-table-row T | x\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  [[ $output == *"no table"* ]]
}

# ------------------------------------------------- pass-through and no rescan

@test "diff hunks and near-miss prefixes pass through byte-for-byte" {
  write_design
  {
    printf '@@ -1,3 +1,4 @@ context\n'
    printf '@@file\n'
    printf '@@filex y\n'
    printf ' @@file indented\n'
    printf '@@design-sectionX Final\n'
    printf '@@@@file z\n'
    printf '@@design-table-rowFinal | x\n'
    printf '+added @@file line\n'
  } >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  cmp "$DRAFT" "$BODY"
}

@test "non-directive lines are byte-identical: CRLF, tabs, backslashes, percent, no final newline" {
  write_design
  printf 'a\tb\r\n\\n %%s $HOME `x`\n\n\n  trailing  \nlast without newline' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  cmp "$DRAFT" "$BODY"
}

@test "an empty draft assembles to an empty body" {
  : >"$DRAFT"
  run "$SCRIPT" --in "$DRAFT" --out "$BODY"
  [ "$status" -eq 0 ]
  [ -f "$BODY" ]
  [ ! -s "$BODY" ]
}

@test "directive-looking text inside an excerpt is not expanded again" {
  write_design
  printf '@@design-section Binding decisions\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  grep -qxF '@@file /etc/passwd' "$BODY"
  ! grep -q 'root:' "$BODY"
}

@test "directive-looking text inside an @@file is not expanded again" {
  write_design
  printf 'line one\n@@design-section Final\n@@file /etc/passwd\n' >"$REPO/scratch/inner.md"
  printf 'x\n@@file scratch/inner.md\ny\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  [ "$(cat "$BODY")" = "$(printf 'x\nline one\n@@design-section Final\n@@file /etc/passwd\ny')" ]
}

@test "directives and ordinary lines interleave in draft order" {
  write_design
  printf 'head\n@@design-section Phase 1\nmid\n@@design-section Final\ntail\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  expected=$(printf 'head\nPhase one overview.\n[design: Phase 1]\nmid\nThe last section, no trailing newline.\n[design: Final]\ntail')
  [ "$(cat "$BODY")" = "$expected" ]
}

# ----------------------------------------------------------------------- @@file

@test "@@file copies a repo file verbatim by relative path" {
  printf 'alpha\n\tbeta \\ gamma\n' >"$REPO/scratch/note.txt"
  printf 'x\n@@file scratch/note.txt\ny\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  [ "$(cat "$BODY")" = "$(printf 'x\nalpha\n\tbeta \\ gamma\ny')" ]
}

@test "@@file adds a newline after a file lacking one, and copies an empty file as nothing" {
  printf 'no newline' >"$REPO/scratch/nonl.txt"
  : >"$REPO/scratch/empty.txt"
  printf '@@file scratch/nonl.txt\n@@file scratch/empty.txt\nz\n' >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  [ "$(cat "$BODY")" = "$(printf 'no newline\nz')" ]
}

@test "@@file accepts an absolute path inside the toplevel and a .. path that stays inside" {
  printf 'inside\n' >"$REPO/scratch/in.txt"
  printf '@@file %s/scratch/in.txt\n@@file scratch/../scratch/in.txt\n' "$REPO" >"$DRAFT"
  assemble
  [ "$status" -eq 0 ]
  [ "$(cat "$BODY")" = "$(printf 'inside\ninside')" ]
}

@test "@@file resolves relative paths against the working directory" {
  printf 'cwd file\n' >"$REPO/scratch/only-here.txt"
  printf '@@file only-here.txt\n' >"$DRAFT"
  cd "$REPO/scratch"
  run "$SCRIPT" --in "$DRAFT" --out "$BODY"
  [ "$status" -eq 0 ]
  [ "$(cat "$BODY")" = "cwd file" ]
}

@test "@@file refuses an absolute path outside the git toplevel with exit 3" {
  printf 'SECRET\n' >"$OUTSIDE/creds.txt"
  printf '@@file %s\n' "$OUTSIDE/creds.txt" >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  [[ $output == *"outside the git toplevel"* ]]
  [ ! -e "$BODY" ]
}

@test "@@file refuses a .. path that escapes the git toplevel with exit 3" {
  printf 'SECRET\n' >"$OUTSIDE/creds.txt"
  printf '@@file ../outside/creds.txt\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  [ ! -e "$BODY" ]
}

@test "@@file refuses a symlink inside the repo that points outside, with exit 3" {
  printf 'SECRET\n' >"$OUTSIDE/creds.txt"
  ln -s "$OUTSIDE/creds.txt" "$REPO/scratch/link.txt"
  printf '@@file scratch/link.txt\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  [ ! -e "$BODY" ]
}

@test "@@file refuses a symlinked directory component that points outside, with exit 3" {
  printf 'SECRET\n' >"$OUTSIDE/creds.txt"
  ln -s "$OUTSIDE" "$REPO/scratch/dir-link"
  printf '@@file scratch/dir-link/creds.txt\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
}

@test "@@file refuses paths into .git with exit 3" {
  printf '@@file .git/config\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  [[ $output == *".git"* ]]
  [ ! -e "$BODY" ]
}

@test "@@file refuses a path through a nested .git component (a symlink into .git)" {
  ln -s "$REPO/.git" "$REPO/scratch/gitlink"
  printf '@@file scratch/gitlink/HEAD\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
}

@test "@@file of a missing path or a directory exits 3" {
  printf '@@file scratch/nope.txt\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
  printf '@@file scratch\n' >"$DRAFT"
  assemble
  [ "$status" -eq 3 ]
}

@test "@@file outside any git work tree is refused with exit 3 (fail closed)" {
  printf 'plain\n' >"$OUTSIDE/plain.txt"
  printf '@@file plain.txt\n' >"$OUTSIDE/draft.md"
  cd "$OUTSIDE"
  run "$SCRIPT" --in "$OUTSIDE/draft.md" --out "$OUTSIDE/body.md"
  [ "$status" -eq 3 ]
  [[ $output == *"not inside a git work tree"* ]]
  [ ! -e "$OUTSIDE/body.md" ]
}

@test "a draft with no @@file directive assembles outside a git work tree" {
  write_design
  cp "$DESIGN" "$OUTSIDE/design.md"
  printf '@@design-section Final\n' >"$OUTSIDE/draft.md"
  cd "$OUTSIDE"
  run "$SCRIPT" --design "$OUTSIDE/design.md" --in "$OUTSIDE/draft.md" --out "$OUTSIDE/body.md"
  [ "$status" -eq 0 ]
  [ "$(cat "$OUTSIDE/body.md")" = "$(printf 'The last section, no trailing newline.\n[design: Final]')" ]
}
