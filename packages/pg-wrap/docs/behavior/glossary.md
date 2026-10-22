# Glossary — pg-wrap

Vocabulary for pg-wrap, a standalone wrapper tool. Any program that invokes pg-wrap, and any
program it wraps, is out of this set's extent and is named here only by role: "a caller", "a
launcher slot" and "a payload".

## Parties

- **Caller** — whatever invokes pg-wrap and asks it to run a payload, check a spec, or show its
  layers. pg-wrap does not know what the caller is (`INV-ISO-1`).
- **Launcher slot** — a place in some other program that accepts only a program path to run, not a
  command line. It is served by the second binary (`INTF-EXEC`).
- **Payload** — what a run ultimately executes: the args after `--` for `run`, or all the args
  `pg-wrap-exec` received, preceded by the exec prefix when the spec has one (`INV-SPEC-5`).
  pg-wrap never inspects it (`INV-ISO-1`).

## The spec

- **Wrap spec** — pg-wrap's own configuration file, a TOML document read through `--spec <file>` or
  through the spec-path variable (`INV-SPEC-1`). It is separate from any caller's configuration; a
  caller names a spec to pg-wrap and pg-wrap reads it.
- **Spec-path variable** (`PG_WRAP_SPEC`) — the environment variable `pg-wrap-exec` reads the spec
  path from. pg-wrap removes it from the payload's environment in every mode (`INV-ENTRY-5`).
- **Schema version** — the required integer `schema_version` in a spec. A version newer than the
  running pg-wrap supports is refused (`INV-SPEC-2`).
- **Wrap entry** — one element of the spec's ordered `wrap` list, outermost first. Two forms
  (`INV-SPEC-3`):
  - **String entry** — shell-parsed, so `$VAR` expands inside the previous wrapper's environment.
  - **Array entry** — argv form: executed directly with the rest appended, never shell-parsed.
- **Strict environment** — the spec's `strict_env` flag: start from the minimal environment instead
  of the inherited one (`INV-ENV-1`).
- **Keep list** — the spec's `keep`: variable names kept in addition to the minimal environment, by
  exact name and never by prefix (`INV-SPEC-4`).
- **Exec prefix** — the spec's `exec`: an argv prefix, absolute first element, put in front of the
  payload's args (`INV-SPEC-5`).

## Composition

- **Layer** — what one wrap entry becomes: a command that ends in "the next layer" (`INV-COMP-1`).
  The innermost layer's rest is the payload.
- **Minimal environment** — `HOME`, `USER`, `LOGNAME`, `TMPDIR`, `LANG`, `TERM` and `COLORTERM`
  where set, and `PATH=/usr/bin:/bin:/usr/sbin:/sbin` (`INV-ENV-1`).
- **Wrapper contract** — the obligations (`INV-WC-1` to `INV-WC-9`) every layer a spec names obeys,
  and which pg-wrap itself obeys as a layer.

## Failure

- **Stage** — where pg-wrap's own failure happened: `spec`, `env`, `layer <i>` or `exec`
  (`INV-FAIL-2`).
- **Exit 97** — the fixed code of every failure pg-wrap itself detects, always before the payload
  starts (`INV-FAIL-1`).
