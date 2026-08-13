# shellcheck shell=bash
# behavior-ids.bash — THE typed-id family list for the behavior-docs conformance
# evaluators. The single definition; every script in all three skills of this
# plugin (intra / inter / impl) sources it instead of spelling the list out.
#
# WHY THIS FILE EXISTS (bead pg2-fbxdw). The list used to be written out at EIGHT
# sites across SIX scripts with nothing checking that they agreed, and it drifted
# TWICE in a row: WS-1 widened it for `USECASE` and touched 2 of the 8; pg2-rlu3m
# widened it for `DEC`/`IMPL` and touched the SAME 2. Both times the other 6 kept
# the old list and silently UNDER-DETECTED the new family — no crash and no false
# finding, just a checker that cannot see the id (`reconcile-imports.sh` reporting
# "owner defines 0 element(s)" for a decisions area; `trace-extract.sh` unable to
# flag a dangling `DEC-` reference at all). Two identical recurrences make the
# third a certainty, so the list is defined ONCE, here.
#
# POLICY (RFC 2119):
#
#   - A script needing the family list MUST source this file and use the variables
#     below. It MUST NOT spell the alternation out locally, not even "temporarily".
#     `checks.test-behavior-docs-id-family-single-definition` in the repo's
#     `flake.nix` FAILS the build naming any file that re-inlines it, which is what
#     makes this a single definition rather than a convention.
#   - A family MUST NOT be added speculatively. The admitted set is exactly the set
#     some area DEFINES: the eight `behavior-docs/docs/behavior/invariants.md`'s
#     INV-3 enumerates for behavior elements, plus the two every
#     `docs/decisions/README.md`'s "Entry ids" defines (`DEC-<TOPIC>-<n>` settled,
#     `IMPL-<n>` captured-but-not-decided, citable across a seam per GOAL-5).
#   - An unrecognized family MUST reach a loud per-row FAIL in the calling script
#     rather than be quietly admitted by a catch-all prefix pattern. Widening this
#     list MUST NOT become "admit any uppercase prefix".
#
# WHAT A MISSING FAMILY COSTS, which is why the list is load-bearing rather than
# cosmetic. The two symptom shapes are different and neither is loud:
#   - In `self-checks.sh` it is a FALSE FAILURE, not a blind spot: a definition
#     line whose family the regex does not know matches no ID, so its UUID carrier
#     reads as an ORPHAN (a carrier with no ID on its line) and the UUID section
#     FAILs on a conformant set.
#   - Everywhere else it is UNDER-DETECTION: the family is skipped, so a real
#     defect in a citation of that family passes silently. Blind spots are
#     symmetric across the evaluators, so nothing reads as WRONG — the gate simply
#     cannot cover the family, which is the quieter and longer-lived failure.

# The families themselves, as a regex alternation. This is the ONE place the set is
# enumerated; both patterns below are derived from it.
BEHAVIOR_ID_FAMILIES='INV|GOAL|STORY|USECASE|JOURNEY|INTF|ACTOR|OQ|DEC|IMPL'

# The typed-id pattern WITHOUT word boundaries — the awk-safe form. Pass it into an
# awk program with `-v idpat="$BEHAVIOR_IDPAT"`; it contains no backslash, so awk's
# `-v` escape processing leaves it byte-identical.
BEHAVIOR_IDPAT="(${BEHAVIOR_ID_FAMILIES})-[A-Za-z0-9]+(-[A-Za-z0-9]+)*"

# The same pattern ANCHORED on word boundaries — the `grep -E` form, which needs
# `\b` so a citation embedded in prose or a code span matches at its own edges.
# `\b` is a GNU ERE extension and is NOT portable into awk, which is exactly why
# the two shapes are separate variables rather than one.
# shellcheck disable=SC2034  # consumed by the scripts that source this library, not here
BEHAVIOR_IDRE="\\b${BEHAVIOR_IDPAT}\\b"
