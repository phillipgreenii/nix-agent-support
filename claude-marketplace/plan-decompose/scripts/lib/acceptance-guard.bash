# shellcheck shell=bash
# acceptance-guard.bash
#
# Sourced by create-packet.sh and update-packet.sh: the ONE implementation of
# the "acceptance lives only in the acceptance field" guard (operator ruling,
# Phillip, 2026-10-13: "only the aceptance field needs to hold the
# acceptance"; speedup plan P5). 22 of 118 packets across 9 dockets repeated
# their acceptance block in the description, whole-block copies made by the
# decomposer.
#
# acceptance_guard_check <body-file> <acceptance-text>
#   Returns 0 when the body is clean. Returns 1 when the check itself could not
#   run (awk missing or failing, body unreadable): it NEVER passes in that case,
#   and callers MUST treat any status other than 0 and 3 as a hard error.
#   Returns 3 after printing, to stderr, a
#   message that quotes the first matches when EITHER
#     1. the body has an "Acceptance criteria" heading (an ATX "#" heading, a
#        numbered and/or bold/emphasised line, optionally ending in ":")
#        followed, before the next heading, by a checklist item ("- [ ] ..."), OR
#     2. at least 2 whole acceptance lines appear as whole lines in the body.
#        Both sides are compared after stripping a leading "- [ ] " (or "[ ] ",
#        also "[x]") and surrounding whitespace; blank lines never count. A
#        single matching line (an AC restating a verbatim Binding decision, say)
#        passes. Acceptance lines with no alphanumeric character (code fences,
#        "---" rules) are generic and never enter the match set.
#   An empty <acceptance-text> disables rule 2 only; rule 1 still applies.
#
# A library: no shebang, no strict-mode line, no top-level code.

acceptance_guard_check() {
  local body_file="$1" acceptance="${2-}" report
  # The assignment is tested directly: callers run this under `|| ...`, which
  # disables errexit inside the function, so an awk failure must be caught here
  # or the guard would silently pass.
  if ! report="$(
    ACCEPTANCE_TEXT="$acceptance" awk '
      function norm(s) {
        sub(/\r$/, "", s)
        sub(/^[[:space:]]*(-[[:space:]]+)?\[[ xX]\][[:space:]]+/, "", s)
        sub(/^[[:space:]]+/, "", s)
        sub(/[[:space:]]+$/, "", s)
        return s
      }
      function is_ac_heading(s,   t) {
        t = tolower(s)
        sub(/\r$/, "", t)
        sub(/^[[:space:]]*#+[[:space:]]*/, "", t)
        sub(/^[[:space:]]*[0-9]+[.)][[:space:]]+/, "", t)
        gsub(/[*_]/, "", t)
        sub(/:?[[:space:]]*$/, "", t)
        sub(/^[[:space:]]+/, "", t)
        return t == "acceptance criteria"
      }
      function is_boundary(s) {
        return s ~ /^[[:space:]]*#+[[:space:]]/ || s ~ /^[[:space:]]*[0-9]+[.)][[:space:]]+\*\*/
      }
      BEGIN {
        n = split(ENVIRON["ACCEPTANCE_TEXT"], lines, "\n")
        for (i = 1; i <= n; i++) {
          k = norm(lines[i])
          if (k ~ /[[:alnum:]]/) ac[k] = 1
        }
      }
      {
        raw = $0
        if (in_ac) {
          if (is_boundary(raw) && !is_ac_heading(raw)) {
            in_ac = 0
          } else if (raw ~ /^[[:space:]]*[-*][[:space:]]+\[[ xX]\]/ && !heading_hit) {
            heading_hit = 1
            heading_line = heading
            heading_item = raw
          }
        }
        if (is_ac_heading(raw)) { in_ac = 1; heading = raw }
        k = norm(raw)
        if (k != "" && (k in ac) && !(k in seen)) {
          seen[k] = 1
          m++
          if (m <= 3) first[m] = raw
        }
      }
      END {
        if (heading_hit) {
          printf "heading: %s\n  followed by checklist item: %s\n", heading_line, heading_item
        }
        if (m >= 2) {
          printf "%d acceptance lines repeated in the body; first matches:\n", m
          for (i = 1; i <= (m < 3 ? m : 3); i++) printf "  %s\n", first[i]
        }
      }
    ' "$body_file"
  )"; then
    echo "acceptance guard: could not check $body_file (awk failed or is missing)" >&2
    return 1
  fi
  if [[ -n $report ]]; then
    {
      echo "acceptance guard: the packet body repeats its acceptance criteria."
      echo "Acceptance criteria belong ONLY in the acceptance field (bd show prints"
      echo "them under ACCEPTANCE CRITERIA); remove them from the body ($body_file):"
      echo "$report"
    } >&2
    return 3
  fi
  return 0
}
