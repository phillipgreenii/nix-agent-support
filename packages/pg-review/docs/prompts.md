# Built-in prompts and prompt assembly

Assets live in `internal/builtin/assets/prompts/` (embedded by `internal/builtin`):
`base.md`, `agent-workflow.md`, `config-health.md`. `internal/prompt` assembles them.

## Shape

- `base.md` holds rules 1 to 3 and 5 to 11 and the line `{lens_rule_4}` where rule 4 sits.
- A lens prompt's first paragraph (up to the first blank line) is that lens's rule 4 text; the rest
  (the closed list of kinds and the calibration examples) is appended after the base.
- Placeholders substituted in one pass: `{lens_rule_4}`, `{max_findings}`, `{scope}`, `{window}`,
  `{staging_path}`. A `RUN` section at the end of the base carries scope, window and staging path.
- Known topics are appended under `KNOWN TOPICS (data)`, one line each (whitespace collapsed, so a
  topic cannot start a new line). They are data under rule 1.
- The prompt hash is the SHA-256 hex of the assembled text.
- A `config.toml` `prompt` or `base_prompt` path is read from `$XDG_CONFIG_HOME/pg-review/` first
  (absolute paths as given), then from the embedded assets.
- `prompt.Repair` builds the reply sent after a validation failure; it lists every failure given.

## Choices recorded

- The design supplies no calibration example text. The examples in the lens prompts are authored
  here and are SYNTHETIC.
- Rule 4 is written without its number; the base supplies "4.".
- Goldens: `testdata/prompt/<lens>.golden.txt`, regenerated with `go test ./internal/prompt -update`.
