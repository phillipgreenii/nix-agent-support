# Linux TODO

Work that can only be done on an `x86_64-linux` machine. The authoring machine is
`aarch64-darwin` with no Linux builder, so each item below is handed to a Linux host.
Remove an item (or this file) once it is done and landed.

## 1. Verify the embedded help hashes on x86_64-linux

Origin: bead `pg2-rrzxh` (discovered from `pg2-kf6xl`).

### Why

`packages/claude-extended-tool-approver/internal/embeddedspecs/data/help-hashes.json`
is the shared baseline and is also the Linux baseline. `pg2-cjfpy.2` (`0b889fc9`) and
`pg2-kf6xl` (`cd273f80`) recorded new values into it **on aarch64-darwin**, so the
Linux check has never confirmed them:

- 18 new names: `gh`, `rg`, `fd`, `bats`, `prek`, `lsof`, `du`, `bgcheck`, `wtdone`,
  `handoff-create`, `session-mode`, `integrate-branch-support`, `pg-go-mutate`,
  `pg-ccaudit`, `pg-wi-flow`, `pg-hooks`, `claude-extended-tool-approver`, and a few
  others.
- `bd`, `curl` and `rtk` were re-recorded.

If any of these prints different `--help` text on Linux, the check fails and the
shared baseline is wrong for Linux.

### Steps

1. Check out `main` of this repo on the Linux host, at or after the commit that added
   this file.
2. Build the check (run it in the background; it can outlast a default 2 minute
   timeout):

   ```bash
   nix build .#checks.x86_64-linux.claude-extended-tool-approver-spec-help-drift
   ```

3. Expected result: the build succeeds and the log ends with
   `spec-drift-check: no drift`.

### If it fails

The check lists each drifting command as `spec-drift-check: <name>: <reason>` and
prints a re-record hint. A reason of `binary not on PATH` means a package is missing
from the check's `nativeBuildInputs` in `flake.nix`
(`claude-extended-tool-approver-spec-help-drift`), which is a different problem from
drift.

For genuine drift:

1. From `packages/claude-extended-tool-approver`, in a shell that has the same tools
   as the check's `nativeBuildInputs` on `PATH`, run:

   ```bash
   claude-extended-tool-approver spec-drift-check --embedded --record
   ```

   On Linux this rewrites the shared baseline `help-hashes.json` in place. Format the
   file with the repo formatter (`nix fmt -- <file>`).

2. Re-record on a darwin host afterwards. `help-hashes.darwin.json` holds only the
   hashes that differ from the shared baseline, so changing the baseline can change
   what the overlay must contain. Until then the darwin check can fail.
3. Commit the regenerated data and re-run the check on both platforms.

### Done when

`nix build .#checks.x86_64-linux.claude-extended-tool-approver-spec-help-drift`
passes on the Linux host, with the baseline committed. Record the outcome in the
commit message, or in a new bead labelled `agent-support` if you found drift you did
not fix.

### Also worth doing while on Linux

These were noted on `pg2-rrzxh` and are not part of the check above:

- `npm`: its recorded hash embeds a `nodejs` store path, so it breaks on every `nodejs`
  rebuild. Consider normalizing the captured text before hashing.
- `curl.json`: the provenance still says curl 8.21.0, but its flags were verified
  against 8.22.0. Fixing that needs a Go registry change plus regenerated data.
