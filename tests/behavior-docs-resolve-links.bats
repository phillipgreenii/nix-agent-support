#!/usr/bin/env bats
# bats file_tags=type:integration
# resolve-links.sh mechanical coverage (bead pg2-2oupw) -- the DEREFERENCING
# half of D5's `[<uuid>](<url>)` imports-table links. resolve-imports.sh only
# PARSES the link (tested in tests/behavior-docs-inter-conformance.bats); this
# file drives the follow-through: does the url's target still carry the UUID,
# locally first, then remotely, WARN (never FAIL) if neither. See
# resolve-links.sh's own header for the Q1/Q2 rulings and for why this check
# is deliberately separate from tests/behavior-docs-real-corpus.sh.

setup() {
  WS="$BATS_TEST_TMPDIR/ws"
  mkdir -p "$WS"
}

# impl_repo <org/repo> -- sets IMPL to a freshly git-init'd repo with that
# origin, so `find_local_checkout` can match it against a url's org/repo.
impl_repo() {
  IMPL="$WS/impl"
  mkdir -p "$IMPL"
  git -C "$IMPL" init -q
  git -C "$IMPL" remote add origin "git@github.com:$1.git"
}

# d5_table <name> <what-it-is> <opath> <uuid> <url> -- writes a one-row D5
# imports table to $IMPL/README.md.
d5_table() {
  cat >"$IMPL/README.md" <<MD
# Implementer

## External references

| Name | What it is | Owner set-path | Owner UUID |
| ---- | ---------- | -------------- | ---------- |
| $1 | $2 | \`$3\` | [$4]($5) |
MD
}

@test "local: url's target repo is THIS repo's own checkout, uuid present -> ok (local)" {
  impl_repo "myorg/myrepo"
  mkdir -p "$IMPL/docs/behavior"
  printf '# Owner\n- INV-1 <!-- uuid: 11111111-1111-4111-8111-111111111111 -->\n' >"$IMPL/docs/behavior/invariants.md"
  d5_table '`INV-1`' 'a rule' 'owner · docs/behavior' \
    '11111111-1111-4111-8111-111111111111' \
    'https://github.com/myorg/myrepo/blob/main/docs/behavior/invariants.md'
  run resolve-links "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*resolves locally'
  echo "$output" | grep -q '1 link(s) checked, 0 WARN'
}

@test "sibling: url's target repo is a DIFFERENT sibling repo under the discovered workspace root -> ok (local)" {
  touch "$WS/pn-workspace.toml"
  impl_repo "myorg/impl-repo"
  SIB="$WS/sib-repo"
  mkdir -p "$SIB/target"
  git -C "$SIB" init -q
  git -C "$SIB" remote add origin git@github.com:otherorg/sib-repo.git
  printf 'carries INV-9 <!-- uuid: 22222222-2222-4222-8222-222222222222 -->\n' >"$SIB/target/file.md"
  d5_table '`INV-9`' 'a cross-repo rule' 'other · target' \
    '22222222-2222-4222-8222-222222222222' \
    'https://github.com/otherorg/sib-repo/blob/main/target/file.md'
  run resolve-links "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*resolves locally'
}

@test "same org/repo prefers THIS checkout over a same-named sibling elsewhere (a worktree may hold edits a sibling scan would not see)" {
  touch "$WS/pn-workspace.toml"
  impl_repo "myorg/myrepo"
  mkdir -p "$IMPL/docs/behavior"
  printf '# Owner\n- INV-1 <!-- uuid: 11111111-1111-4111-8111-111111111111 -->\n' >"$IMPL/docs/behavior/invariants.md"
  # A DECOY sibling that also claims to be myorg/myrepo but does NOT carry the
  # uuid -- if the scan preferred it over $IMPL, this would misreport a WARN.
  DECOY="$WS/decoy"
  mkdir -p "$DECOY/docs/behavior"
  git -C "$DECOY" init -q
  git -C "$DECOY" remote add origin git@github.com:myorg/myrepo.git
  printf 'no uuid here\n' >"$DECOY/docs/behavior/invariants.md"
  d5_table '`INV-1`' 'a rule' 'owner · docs/behavior' \
    '11111111-1111-4111-8111-111111111111' \
    'https://github.com/myorg/myrepo/blob/main/docs/behavior/invariants.md'
  run resolve-links "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*resolves locally'
}

@test "no sibling found, no network reachable -> WARN, exit still 0 (never a FAIL)" {
  impl_repo "myorg/impl-only"
  d5_table '`INV-GHOST`' 'nothing resolves' 'nobody · nowhere' \
    '99999999-9999-4999-8999-999999999999' \
    'https://github.com/some-org/some-repo/blob/main/some/path.md'
  run resolve-links --timeout 3 "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  WARN .*resolves neither locally nor remotely'
  echo "$output" | grep -q '1 WARN'
}

@test "remote fallback: stubbed fetch confirms the uuid -> ok (remote), not a WARN" {
  impl_repo "myorg/impl-remote"
  mkdir -p "$BATS_TEST_TMPDIR/bin"
  cat >"$BATS_TEST_TMPDIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf 'remote content carries 33333333-3333-4333-8333-333333333333\n'
EOF
  chmod +x "$BATS_TEST_TMPDIR/bin/curl"
  d5_table '`INV-REMOTE`' 'only the remote confirms it' 'nobody · nowhere' \
    '33333333-3333-4333-8333-333333333333' \
    'https://github.com/some-org/some-repo/blob/main/some/path.md'
  PATH="$BATS_TEST_TMPDIR/bin:$PATH" run resolve-links "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*resolves remotely only'
  echo "$output" | grep -q '0 WARN'
}

@test "remote fallback: stubbed fetch does NOT carry the uuid -> WARN, never FAIL" {
  impl_repo "myorg/impl-remote2"
  mkdir -p "$BATS_TEST_TMPDIR/bin"
  cat >"$BATS_TEST_TMPDIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf 'remote content carries a DIFFERENT uuid entirely\n'
EOF
  chmod +x "$BATS_TEST_TMPDIR/bin/curl"
  d5_table '`INV-ROTTED`' 'rotted' 'nobody · nowhere' \
    '44444444-4444-4444-8444-444444444444' \
    'https://github.com/some-org/some-repo/blob/main/some/path.md'
  PATH="$BATS_TEST_TMPDIR/bin:$PATH" run resolve-links "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  WARN .*resolves neither locally nor remotely'
}

@test "raw-url conversion: the fetch sees raw.githubusercontent.com, never the blob-rendering url" {
  # A GitHub blob url renders through the markdown renderer, which DROPS html
  # comments -- exactly where every uuid lives (INV-3's carrier convention) --
  # so fetching the blob url itself would systematically miss every uuid. This
  # asserts the CONVERSION happened, not merely that some fetch happened.
  impl_repo "myorg/impl-raw"
  mkdir -p "$BATS_TEST_TMPDIR/bin"
  cat >"$BATS_TEST_TMPDIR/bin/curl" <<'EOF'
#!/usr/bin/env bash
for a in "$@"; do
  case "$a" in
  https://raw.githubusercontent.com/*)
    printf 'ok 55555555-5555-4555-8555-555555555555\n'
    exit 0
    ;;
  esac
done
exit 1
EOF
  chmod +x "$BATS_TEST_TMPDIR/bin/curl"
  d5_table '`INV-RAW`' 'checks the raw conversion' 'nobody · nowhere' \
    '55555555-5555-4555-8555-555555555555' \
    'https://github.com/some-org/some-repo/blob/main/some/path.md'
  PATH="$BATS_TEST_TMPDIR/bin:$PATH" run resolve-links "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -qE '^  ok .*resolves remotely only'
}

@test "bare-uuid row (no D5 link) is skipped -- nothing to dereference" {
  impl_repo "myorg/bare"
  cat >"$IMPL/README.md" <<'MD'
# Implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `INV-BARE` | `owner/docs/behavior` | 66666666-6666-4666-8666-666666666666 |
MD
  run resolve-links "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "nothing to dereference"
}

@test "external-marked row is skipped -- nothing to dereference" {
  impl_repo "myorg/ext"
  cat >"$IMPL/README.md" <<'MD'
# Implementer

## External references

| Name | Owner set-path | Owner UUID |
| ---- | -------------- | ---------- |
| `git` | `(external)` | `(external)` |
MD
  run resolve-links "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q "nothing to dereference"
}

@test "usage error: no argument exits 2, distinct from a finding" {
  run resolve-links
  [ "$status" -eq 2 ]
}

@test "usage error: nonexistent directory exits 2" {
  run resolve-links "$WS/does-not-exist"
  [ "$status" -eq 2 ]
}

@test "never a FAIL exit: multiple WARNs still exit 0" {
  impl_repo "myorg/manywarn"
  cat >"$IMPL/README.md" <<'MD'
# Implementer

## External references

| Name | What it is | Owner set-path | Owner UUID |
| ---- | ---------- | -------------- | ---------- |
| `INV-A` | ghost A | `nobody · nowhere` | [77777777-7777-4777-8777-777777777777](https://github.com/some-org/some-repo/blob/main/a.md) |
| `INV-B` | ghost B | `nobody · nowhere` | [88888888-8888-4888-8888-888888888888](https://github.com/some-org/some-repo/blob/main/b.md) |
MD
  run resolve-links --timeout 3 "$IMPL"
  [ "$status" -eq 0 ]
  echo "$output" | grep -q '2 link(s) checked, 2 WARN'
}

@test "canonical output: multiple table files are processed in sorted filename order, not creation order" {
  impl_repo "myorg/multifile"
  mkdir -p "$IMPL/docs/behavior"
  printf '# Owner\n- Z <!-- uuid: aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa -->\n- Y <!-- uuid: bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb -->\n' >"$IMPL/docs/behavior/invariants.md"
  # Create the ALPHABETICALLY-LATER file FIRST, on purpose -- filesystem
  # creation order and `find`'s own enumeration order are not guaranteed to be
  # byte-sorted, which is exactly the class of drift resolve-imports.sh's own
  # WS-6 closing notes describe (an unsorted glob making a finding list order
  # depend on the filesystem rather than on content).
  cat >"$IMPL/z-refs.md" <<MD
## External references

| Name | What it is | Owner set-path | Owner UUID |
| ---- | ---------- | -------------- | ---------- |
| \`Z\` | in z-refs.md | \`o · docs/behavior\` | [aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa](https://github.com/myorg/multifile/blob/main/docs/behavior/invariants.md) |
MD
  cat >"$IMPL/a-refs.md" <<MD
## External references

| Name | What it is | Owner set-path | Owner UUID |
| ---- | ---------- | -------------- | ---------- |
| \`Y\` | in a-refs.md | \`o · docs/behavior\` | [bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb](https://github.com/myorg/multifile/blob/main/docs/behavior/invariants.md) |
MD
  run resolve-links "$IMPL"
  [ "$status" -eq 0 ]
  first_line=$(echo "$output" | grep -E '^  ok ' | head -1)
  echo "$first_line" | grep -q '`Y`'
}

@test "canonical output: byte-identical under any ambient locale" {
  impl_repo "myorg/locale"
  mkdir -p "$IMPL/docs/behavior"
  printf '# Owner\n- Q <!-- uuid: cccccccc-cccc-4ccc-8ccc-cccccccccccc -->\n' >"$IMPL/docs/behavior/invariants.md"
  d5_table '`Q`' 'locale check' 'o · docs/behavior' \
    'cccccccc-cccc-4ccc-8ccc-cccccccccccc' \
    'https://github.com/myorg/locale/blob/main/docs/behavior/invariants.md'
  a="$BATS_TEST_TMPDIR/out.c"
  b="$BATS_TEST_TMPDIR/out.utf8"
  env LC_ALL=C resolve-links "$IMPL" >"$a" 2>&1 || true
  env LC_ALL=en_US.UTF-8 LC_COLLATE=en_US.UTF-8 resolve-links "$IMPL" >"$b" 2>&1 || true
  diff "$a" "$b"
}
