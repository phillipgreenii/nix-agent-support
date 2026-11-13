# Evidence stage notes

The evidence stage (`internal/runner/evidence.go`) runs between the gate and the analyst.

- **Providers** run in the lens's listed order with the configured command, its words, then the
  flags of the provider's `args_style` (`cwd-prefix`: `--query`, `--cwd-prefix`,
  `--exclude-cwd-prefix`; `inventory`: `--scope-prefix`, `--layer`, `--exclude-cwd-prefix`), then
  `--since`, `--until` (UTC, `Z` suffix) and `--scope`. `--exclude-cwd-prefix` is omitted when
  `[ccpool].neutral_dir` is empty. A non-zero exit, a timeout (`timeout_seconds`, default 300) or
  output that fails the `evidence-rows` schema is a provider error: exit 23 and a Failed run. A
  timeout kills the provider's whole process group.
- **Merge**: rows are redacted (paths under the scope's first prefix become relative), sorted
  byte-wise by (kind, subject key), ties in provider order, and numbered `E-001` onward.
- **Files**: `digest.txt` and `evidence.json` under
  `$XDG_CACHE_HOME/pg-review/evidence/run-<run id>/` (0700 directory, 0600 files); the path is
  written to the run row's `evidence_dir`. Redacted copies go to a `mkdtemp` directory directly
  under `/tmp` (umask 077, never `$TMPDIR`), recorded in `staging_path` as soon as it exists.
- **Prompt hash**: SHA-256 of the assembled prompt, stored in `prompt_hash`. Known topics are not
  passed to the prompt here (the broker and analyst packets own them).
- **Cleanup**: the finish stage deletes staging for every run that is not left AwaitingAnalyst;
  a failed or cancelled run is cleaned by the failure path.
- **End state under `--evidence-only` and `--analyst none`**: the run ends Filed with zero items
  (choice of this packet). The run key therefore counts as done for such a run.
