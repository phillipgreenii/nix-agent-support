#!/usr/bin/env bash
# Identifier allowlist guard (bead pg2-34s486). Body of the `identifier-allowlist`
# pre-commit / pre-land hook; the single enforcement point that replaced six drifting
# per-module Go copies (identifier_allowlist_test.go in pg-connector, pg-decider,
# pg-desk, pg-desk-shadow and pg-router-source-pg-desk, and identifier_guard_test.go in
# work-report; beads pg2-tphcc, pg2-h3db9).
#
# Rule (CLAUDE.md "Public Repository - No ZipRecruiter Disclosure" and its "Mechanical
# guard" paragraph): a public repo MUST carry no user identifier other than the
# operator's own, and NO denylist of forbidden tokens may be committed. So the guard is
# an ALLOWLIST inversion: it flags every identity-shaped token in a structured identity
# field that is NOT on the module's committed allowlist of known-safe identifiers.
# Three shapes are recognised (a text scan, not a parse):
#   json     a JSON key  login | author | reviewer | user  whose string value is not
#            on the allowlist (value trimmed and compared case-insensitively);
#   branch   a branch-name component shaped  name.name.TICKET-NNN  whose  name.name  is
#            not on the allowlist (in practice never allowlistable: allowlist entries
#            are single handles);
#   trailer  a git  Author:  or  Committer:  line whose name is not on the allowlist.
# It does NOT catch a real name in free-text prose with no structural marker; that stays
# a manual-review responsibility.
#
# Per-module config (this file's MODULES table plus one allowlist file per module):
#   scope      which tracked files of the module are scanned: testdata (every file under
#              a testdata/ directory) or "wide" (every .go file, every testdata/ file
#              and every file under a docs/ directory);
#   allowlist  scripts/identifier-allowlists/<module>.txt, one allowed identifier per
#              line, '#' comments and blank lines ignored. To allow a new known-safe
#              identifier for ONE module, add a line (with a comment saying why it is
#              safe) to that module's file only. The files MUST list allowed
#              identifiers, never forbidden ones, and never a real third-party name.
#   canaries   the expected violation count is zero, so "found nothing" must not double
#              as proof the scan ran: each module names files that MUST be scanned, a
#              minimum total file count and optional per-subtree minimums; a rename,
#              a mis-scoped path or an emptied directory FAILS loudly.
#
# Usage: check-identifier-allowlist.sh [--root <repo-root>] [<changed-file>...]
#   Changed files are paths relative to the repo root (what prek hands a hook). With
#   changed files: only the modules containing one (or every module when this script or
#   an allowlist changed) are scanned; none in scope exits 0 at once. With no files:
#   scan every module. Only git-tracked regular files are scanned (so `git add` a new
#   file first). Needs git and ripgrep built with PCRE2 (rg -P).
#
# Exits 0 when clean; 1 listing every violation as `module: file:line: shape: "token"`
# (deduplicated per file and token) or a failed canary / config problem; 2 on usage.
set -euo pipefail

ALLOWLIST_DIR="scripts/identifier-allowlists"
SELF="scripts/check-identifier-allowlist.sh"

# --- THE per-module config: one row per module, so adding a module is one row plus
# one allowlist file. Row format (fields split on '|'; list fields are space separated):
#   name | root | scope | min files | required files (relative to root) | subtree minimums
# Subtree minimums are  <relative-prefix>=<n>  or  kind:go=<n>, kind:testdata=<n>,
# kind:docs=<n>  (files of that kind among the scanned ones).
MODULES=(
  "pg-connector|packages/pg-connector|testdata|60|pkg/scriptout/conformance/testdata/golden/response-success.json pkg/scriptout/conformance/testdata/golden/error.json|"
  "pg-decider|packages/pg-decider|testdata|200|testdata/pr_view_minimal.json|"
  "pg-desk|packages/pg-desk|testdata|70|internal/config/testdata/config.example.yaml|internal/focus/testdata/=25"
  "pg-desk-shadow|packages/pg-desk-shadow|testdata|3||"
  "pg-router-source-pg-desk|packages/pg-router-source-pg-desk|testdata|5|cmd/pg-router-source-pg-desk/testdata/envelope_degraded.json|"
  "work-report|packages/work-report|wide|50||kind:go=40 kind:testdata=5 kind:docs=4 docs/behavior/=4"
)

root=""
files=()
while [ "$#" -gt 0 ]; do
  case "$1" in
  --root)
    root="${2:?--root needs a directory}"
    shift 2
    ;;
  -h | --help)
    sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  --)
    shift
    files+=("$@")
    break
    ;;
  -*)
    echo "check-identifier-allowlist: unknown option: $1" >&2
    exit 2
    ;;
  *)
    files+=("$1")
    shift
    ;;
  esac
done

if [ -z "${root}" ]; then
  root="$(git rev-parse --show-toplevel)"
fi

# Which modules to scan. With changed files, only those inside a module root, or all of
# them when the script or an allowlist changed. Without files, all of them.
selected=()
all_selected=0
if [ "${#files[@]}" -eq 0 ]; then
  all_selected=1
else
  for f in "${files[@]}"; do
    if [ "${f}" = "${SELF}" ] || [[ ${f} == "${ALLOWLIST_DIR}"/* ]]; then
      all_selected=1
      break
    fi
  done
fi
for row in "${MODULES[@]}"; do
  IFS='|' read -r name mroot _ <<<"${row}"
  pick=${all_selected}
  if [ "${pick}" -eq 0 ]; then
    for f in "${files[@]}"; do
      if [[ ${f} == "${mroot}"/* ]]; then
        pick=1
        break
      fi
    done
  fi
  if [ "${pick}" -eq 1 ]; then
    selected+=("${row}")
  fi
done
if [ "${#selected[@]}" -eq 0 ]; then
  exit 0
fi

failures=()
total=0

# Build the PCRE alternation of one allowlist file: trimmed, comments and blanks
# dropped, regex-escaped, joined with '|'. Empty output means no usable entry.
build_alt() {
  local file="$1"
  sed -e 's/#.*$//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' "${file}" |
    sed -e '/^$/d' -e 's/[][\\.*^$(){}+?|/-]/\\&/g' |
    paste -sd'|' -
}

# Lowercase kind test of a path relative to the module root.
has_component() {
  case "/${1}/" in
  */"${2}"/*) return 0 ;;
  *) return 1 ;;
  esac
}

for row in "${selected[@]}"; do
  IFS='|' read -r name mroot scope min required subtrees <<<"${row}"
  allow_file="${root}/${ALLOWLIST_DIR}/${name}.txt"
  if [ ! -f "${allow_file}" ]; then
    failures+=("${name}: allowlist file ${ALLOWLIST_DIR}/${name}.txt is missing")
    continue
  fi
  alt="$(build_alt "${allow_file}")"
  if [ -z "${alt}" ]; then
    failures+=("${name}: allowlist ${ALLOWLIST_DIR}/${name}.txt has no entries (an empty allowlist would flag the operator's own identity; add at least that)")
    continue
  fi

  scan=()
  n_go=0
  n_testdata=0
  n_docs=0
  declare -A sub_count=()
  saw=" "
  while IFS= read -r -d '' f; do
    rel="${f#"${mroot}"/}"
    [ -f "${root}/${f}" ] || continue
    if has_component "${rel}" vendor || has_component "${rel}" node_modules; then
      continue
    fi
    is_go=0
    is_td=0
    is_docs=0
    case "${rel}" in *.go) is_go=1 ;; esac
    has_component "${rel}" testdata && is_td=1
    has_component "${rel}" docs && is_docs=1
    if [ "${scope}" = "wide" ]; then
      [ $((is_go + is_td + is_docs)) -gt 0 ] || continue
    else
      [ "${is_td}" -eq 1 ] || continue
    fi
    scan+=("${f}")
    saw="${saw}${rel} "
    n_go=$((n_go + is_go))
    n_testdata=$((n_testdata + is_td))
    n_docs=$((n_docs + is_docs))
    for sub in ${subtrees}; do
      key="${sub%%=*}"
      case "${key}" in
      kind:*) continue ;;
      esac
      if [[ ${rel} == "${key}"* ]]; then
        sub_count["${key}"]=$((${sub_count["${key}"]:-0} + 1))
      fi
    done
  done < <(git -C "${root}" ls-files -z -- "${mroot}")

  for want in ${required}; do
    if [[ ${saw} != *" ${want} "* ]]; then
      failures+=("canary: ${name}: never scanned ${mroot}/${want} (renamed or moved out from under the guard; update MODULES in ${SELF})")
    fi
  done
  if [ "${#scan[@]}" -lt "${min}" ]; then
    failures+=("canary: ${name}: scanned only ${#scan[@]} file(s), expected at least ${min} (the scan is not covering the module)")
  fi
  for sub in ${subtrees}; do
    key="${sub%%=*}"
    need="${sub##*=}"
    case "${key}" in
    kind:go) got=${n_go} ;;
    kind:testdata) got=${n_testdata} ;;
    kind:docs) got=${n_docs} ;;
    *) got=${sub_count["${key}"]:-0} ;;
    esac
    if [ "${got}" -lt "${need}" ]; then
      failures+=("canary: ${name}: scanned only ${got} file(s) for ${key}, expected at least ${need}")
    fi
  done
  unset sub_count
  total=$((total + ${#scan[@]}))
  [ "${#scan[@]}" -gt 0 ] || continue

  # The three shapes. Case-insensitivity applies ONLY to the allowlist comparison
  # ((?i:...)), never to the JSON keys or the branch shape. TRAP: the quantifier after
  # the colon or opening quote MUST be possessive (\s*+): a plain \s* backtracks past
  # the negative lookahead and flags allowlisted values. Group 1 is the offending token.
  # -U lets \s span lines exactly as the retired Go regexes did.
  rx_json="\"(?:login|author|reviewer|user)\"\\s*:\\s*\"\\s*+(?!(?i:${alt})\\s*\")(?=[^\"]*\\S)([^\"]*)\""
  rx_branch="\\b(?!(?i:${alt})\\.[A-Z][A-Z0-9]*-[0-9]+\\b)([a-z][a-z0-9-]*\\.[a-z][a-z0-9-]*)\\.[A-Z][A-Z0-9]*-[0-9]+\\b"
  rx_trailer="(?m)^\\s*(?:Author|Committer):\\s*+(?!(?i:${alt})\\s*<)([^<\\n]+?)\\s*<"
  for shape in json branch trailer; do
    case "${shape}" in
    json) rx="${rx_json}" ;;
    branch) rx="${rx_branch}" ;;
    trailer) rx="${rx_trailer}" ;;
    esac
    # rg exits 1 on "no match" (the good case) and 2 on an error (a bad pattern is a
    # config problem and MUST fail). A binary file prints a notice instead of matches:
    # the retired guards skipped non-text files, so those lines are dropped.
    set +e
    out="$(cd "${root}" && rg -P -U -n -o --no-heading --with-filename --no-config -r '$1' -e "${rx}" -- "${scan[@]}" 2>&1)"
    rc=$?
    set -e
    if [ "${rc}" -gt 1 ]; then
      failures+=("${name}: rg failed on the ${shape} shape (rc=${rc}): ${out}")
      continue
    fi
    seen=" "
    while IFS= read -r hit; do
      [[ ${hit} =~ ^(.+):([0-9]+):(.*)$ ]] || continue
      hf="${BASH_REMATCH[1]}"
      hl="${BASH_REMATCH[2]}"
      tok="${BASH_REMATCH[3]}"
      tok="${tok#"${tok%%[![:space:]]*}"}"
      tok="${tok%"${tok##*[![:space:]]}"}"
      key="${hf}"$'\x01'"${shape}"$'\x01'"${tok}"
      [[ ${seen} == *" ${key} "* ]] && continue
      seen="${seen}${key} "
      failures+=("${name}: ${hf}:${hl}: ${shape}: \"${tok}\" is not on ${ALLOWLIST_DIR}/${name}.txt")
    done <<<"${out}"
  done
done

if [ "${#failures[@]}" -gt 0 ]; then
  for msg in "${failures[@]}"; do
    echo "check-identifier-allowlist: ${msg}" >&2
  done
  echo "FAIL: ${#failures[@]} problem(s). A real person's or organization's identifier MUST NOT be committed to this public repo (CLAUDE.md \"Public Repository - No ZipRecruiter Disclosure\"); for a known-safe placeholder or public bot/product name add one line, with a comment saying why it is safe, to that module's file under ${ALLOWLIST_DIR}/." >&2
  exit 1
fi
echo "OK: ${total} file(s) scanned across ${#selected[@]} module(s); every identity-shaped token is on its allowlist." >&2
