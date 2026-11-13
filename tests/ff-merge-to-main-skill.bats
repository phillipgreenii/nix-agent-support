#!/usr/bin/env bats
# bats file_tags=type:unit
# Pins the recipe text of the integrate-branch ff-merge-to-main skill: --repo on every
# pg-git-lander command, the single long --wait, the never-leave-a-land-stopped rule and the
# launcher PATH statement (operator rulings, Phillip, 2026-11-13).
#
# Environment: FFS_SKILL may point at the SKILL.md under test (the flake check sets it).

setup() {
  SKILL="${FFS_SKILL:-${BATS_TEST_DIRNAME}/../claude-marketplace/integrate-branch/skills/ff-merge-to-main/SKILL.md}"
  [ -f "$SKILL" ]
}

@test "land recipe names the repo with --repo" {
  grep -qF 'pg-git-lander land <branch> --repo <repo-root> --source-worktree <worktree> --json' "$SKILL"
  grep -qF -- '--wait 20m' "$SKILL"
  grep -qF 'pg-git-lander land "$FB" --repo "$CC" --source-worktree "$WT" --wait 20m --json' "$SKILL"
  grep -qF 'pg-git-lander land FB --repo CC --source-worktree WT --wait 20m --json' "$SKILL"
}

@test "rebase names the repo with --repo" {
  grep -qF -- 'pg-git-lander rebase "$FB" --onto "$PRIMARY" --repo "$CC"' "$SKILL"
}

@test "every resume flag is paired with --repo" {
  for flag in --continue --skip --abort --quit; do
    grep -qF -- "$flag --repo \"\$CC\"" "$SKILL" || {
      echo "missing $flag --repo" >&2
      return 1
    }
  done
  grep -qF '`land`, `rebase` and every resume' "$SKILL"
  grep -qF -- '`--repo <repo-root>`' "$SKILL"
}

@test "one long wait replaces retry-twice-then-ask" {
  grep -qF -- '`--wait 20m`' "$SKILL"
  grep -qF 'stopped-pending' "$SKILL"
  ! grep -qF 'at most twice' "$SKILL"
}

@test "an agent never leaves a land stopped and aborts after tracking" {
  grep -qF '**Never leave a land stopped.**' "$SKILL"
  grep -qF 'pg-git-lander land <branch> --abort --repo <repo-root>' "$SKILL"
  grep -qF 'MUST first track' "$SKILL"
  grep -qF -- '`--continue` is allowed only for a stop the agent will resolve right away' "$SKILL"
}

@test "launcher PATH statement is present" {
  grep -qF 'is on the user `PATH` so a person can use it' "$SKILL"
  grep -qF 'MUST list' "$SKILL"
  grep -qE '`pg-git-lander` explicitly' "$SKILL"
}

@test "no bead ids in the skill" {
  ! grep -qE '\bpg2-[a-z0-9]+' "$SKILL"
}

@test "no command line in the skill uses a resume flag without --repo" {
  # A line that runs pg-git-lander with a resume flag (or a table/diagram cell that names the
  # command with one) MUST also carry --repo, or CC in the diagram.
  run bash -c 'grep -nE -- "pg-git-lander [a-z]+ .*--(continue|skip|abort|quit)|land (\"\\\$FB\"|FB) --(continue|skip|abort|quit)" "$1" | grep -vE -- "--repo|CC"' _ "$SKILL"
  [ -z "$output" ] || {
    echo "resume flag without --repo:" >&2
    echo "$output" >&2
    return 1
  }
}

@test "exit 75 is never described as host load" {
  ! grep -qiE 'host.load|load average' "$SKILL"
}
