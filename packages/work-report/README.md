# work-report

work-report records the operator's own activity and renders reports from it. It pulls activity
entries from the sources that `pg-connector` exposes, appends them to a local SQLite store,
resolves the latest observation per entry id, and runs a report **generator** over the entries of
a range.

The intended behavior is in [docs/behavior](docs/behavior/README.md) (start there for the
guarantees). The wire shapes, exit codes, paths and file names of the generator contract are in
[ADR 0102](../../docs/adr/0102-work-report-runs-external-generators-under-a-versioned-process-contract.md).

## Commands

| Command                              | What it does                                                                         |
| ------------------------------------ | ------------------------------------------------------------------------------------ |
| `work-report pull`                   | Pull activity from pg-connector into the store (exit 2 when a source degraded).      |
| `work-report query`                  | List the stored entries for a range, optionally narrowed by label, source and type.  |
| `work-report report [<preset>]`      | Run a report preset (default `baseline`) and write its files into a run directory.   |
| `work-report status`                 | Show per-source pull health and store size. It does not report on reports.           |
| `work-report config show`            | Print the effective configuration, including generators and presets.                 |
| `work-report config validate`        | Check the config, run every preset's generator in validate mode, check pg-connector. |
| `work-report config pg-router-query` | Print the pg-router `[[query]]` stanza that schedules the pull.                      |

Global flags: `--config FILE`, `--output json|human`, `--store FILE`. Range forms are listed by
`work-report report --help` (for example `today`, `yesterday`, `last-week`, a single date, or a
span).

## Generators and presets

Reporting is split in two. work-report owns everything around a run: it resolves the range and the
narrowing, selects the matching entries, creates the output directory, checks what the generator
claims it wrote, and records the run. A **generator** owns only the rendering.

- A **generator** is an external command registered under `generators:` in the config. It is run as
  an argv vector (never through a shell), receives one JSON envelope on stdin and prints one JSON
  manifest on stdout. Any language works.
- A **preset** is a named report definition under `reports:`. It binds a generator, that
  generator's `config` object, a `range`, a `narrowing` and an optional `output.dir`.
- The built-in `baseline` generator and `baseline` preset need no registration and always work, even
  when the rest of the config is broken. They write one plain chronological markdown file,
  `baseline.md`. The name `baseline` MUST NOT be defined in the config.

```yaml
# $XDG_CONFIG_HOME/work-report/config.yaml (an excerpt)
generators:
  narrative:
    command: [work-report-narrative]
    timeout: 10m # default 5m
    inputs: [baseline] # also send the baseline text in the envelope
    env: [HOME] # extra environment variable NAMES passed through
reports:
  example-weekly:
    generator: narrative
    range: last-week # quote a date range: an unquoted 2026-03-01 is a YAML timestamp
    narrowing:
      labels: []
      sources: [example-source]
      types: []
    config:
      model: sonnet
    output:
      dir: ~/reports/example-weekly
```

Run it with `work-report report example-weekly`. A flag replaces the preset field of the same name
(`--range`, `--label`, `--source`, `--type`, `--output-dir`). Narrowing composes AND across
dimensions and OR within one. `--generator NAME` runs a registered generator ad hoc, with an empty
config and no preset; it cannot be combined with a preset name. `--print` writes the single text
report to stdout (summary, warnings and file list go to stderr) and cannot be combined with
`--output json`.

The `generators:` and `reports:` sections are checked only by `report`, `config show` and
`config validate`, so a mistake there never affects `pull`, `query` or `status`. `config validate`
checks the presets in name order and runs each generator in validate mode under the same
environment allowlist as a real run.

### The process contract in brief

- stdin is the envelope: the contract version, the mode (`generate` or `validate`), the range, the
  narrowing, exactly the matching entries, a per-source footer, the optional baseline text, the
  preset's config and the absolute output directory. A generator MUST NOT re-filter the entries and
  MUST treat entry text as untrusted data.
- stdout is the manifest: the contract version and a non-empty list of files, each with a relative
  path inside the output directory and a content type.
- The generator's environment is an allowlist (`PATH`, `HOME`, `USER`, `LANG`, `TMPDIR`, the XDG
  state, config and cache directories, the names in the generator's `env` list, and
  `WORK_REPORT_CONTRACT` and `WORK_REPORT_MODE`), never the caller's environment.
- Generator exit codes: 0 success, 1 unexpected failure, 2 usage error, 3 invalid generator config.
- The timeout covers the whole run. On expiry the process group gets `SIGTERM`, then `SIGKILL`.

The full envelope, the manifest rules, the outcome kinds and the run record are in ADR 0102.

### Where a report goes

Each run gets its own directory `<UTC timestamp>-<4 hex>` under the preset's output directory
(default `$XDG_STATE_HOME/work-report/reports/<preset>`, and `reports/adhoc` for `--generator`).
It holds the generator's files, `stderr.log` and `manifest.json`, the run record; a run without
`manifest.json` did not finish. A failed run keeps its directory. After a successful run the
`latest` symlink in the output directory points at it.

An operator finds a report through the output directory or the `latest` symlink. `status` no longer
reports on reports, and the store keeps no report history. work-report never prunes run
directories.

### Outcomes and exit codes

| Command           | Exit | Meaning                                                                                    |
| ----------------- | ---- | ------------------------------------------------------------------------------------------ |
| `report`          | 0    | A report was produced.                                                                     |
| `report`          | 2    | Usage error (a flag, an argument, a bad range or `--output` value).                        |
| `report`          | 3    | The run did not produce a report; the outcome names the reason and the kept run directory. |
| `config validate` | 0    | No problem found.                                                                          |
| `config validate` | 3    | Any problem (all are printed, not the first).                                              |
| `pull`            | 2    | Some sources degraded (so a bad flag stays exit 1).                                        |

Outcome kinds are `ok`, `timeout`, `cancelled`, `exit_nonzero`, `config_invalid`,
`invalid_manifest`, `not_found`, `contract_version`, `stdout_cap` and `output_dir_unwritable`.

## work-report-narrative

`work-report-narrative` is the second binary this package builds, and the one generator that ships
with it. It hands the baseline text to `claude -p` as data and writes `report.md`. It is registered
by the home-manager module under the name `narrative` but no preset uses it until the deployment
defines one.

Its preset `config` keys are all optional: `model`, and the three prompt parts `prompt`,
`promptPrepend` and `promptAppend`, each with a `...File` form that takes an absolute path (a key
and its `...File` form are mutually exclusive). The prompt is assembled as a fixed preamble that
declares the activity data untrusted, the optional prepend, the task section (replaced by
`prompt`), the optional append, and fixed closing rules. Configuration can change the layout and
emphasis of the report, never the injection guard. The generator never writes a fabricated or
fallback report: every failure is a non-zero exit with the reason on stderr.

## Breaking changes from the previous release

- `work-report report --kind` is gone and fails with migration guidance; define a preset instead.
- `work-report report --out FILE` is gone. Use `work-report report --print > FILE` for a single
  text report, or read the files in the run directory.
- A report writes files into a run directory. It no longer prints the report on stdout by default.
- The `kinds.narrative.*` config keys are retired, and a config that still carries `kinds` fails to
  load with the migration text. `kinds.narrative.model` becomes `reports.<name>.config.model`, and
  `kinds.narrative.systemPromptFile` becomes `reports.<name>.config.promptFile` (it now replaces
  only the task section; the injection guard is fixed).
- A report outcome and `config validate` problems exit 3, not 1.
- The baseline header no longer carries `Kind: baseline`.
- The telemetry `report.jsonl` key set changed.
- The Go `Generate(...)` interface in `internal/report` and `internal/narrative` was replaced by the
  runner's process contract.

### Schema version 2 is a one-way change

The store moves to **schema version 2**, which drops the `report` table and the `last_report` meta
key (existing report rows are discarded). The migration cannot be undone: after the store has been
migrated, **an older binary refuses the database** and exits with an error rather than reading it.
To go back to an older binary, restore the store from a copy taken before the first run of this
version, or let the older binary start a new store.

## Packaging

The flake package `work-report` installs both binaries; `work-report` is wrapped so that
`pg-connector` is on its `PATH`. `work-report-narrative` is not wrapped: it execs only `claude`,
which the operator installs. The home-manager module `phillipgreenii.programs.work-report` renders
`config.yaml` from typed options, including `generators` (the `narrative` generator is registered
by default, field by field at default priority) and `reports`.

## Development

```bash
cd packages/work-report
nice -n 10 go test -p 4 ./...
```

Docs and fixtures MUST NOT name organization-specific identifiers: the package's identifier guard
test (`identifier_guard_test.go`) walks every `.go` file and every file under `docs/` and
`testdata/`. Use `example-*` names.
