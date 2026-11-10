# Interpreters and dialects: the registered set, and where to find evidence for each

`specfmt.Validate` fail-closed rejects any `CommandSpecV1.Interpreter` or `program`-role
`OperandRoleV1.Dialect` spelling that is not one of these registered names
(`cmddesc.LookupInterpreter`/`LookupDialect`, `internal/specfmt/validate.go`). Never invent a new
spelling — if a command's semantics genuinely need one that does not exist yet, that is a gap to
flag (comment on the packet, or escalate per its lifecycle bounds), not something this skill can
add on its own (`internal/cmddesc` is out of this skill's Files scope — it only CONSUMES the
registered names).

## Command-level interpreters (8, plus the generic default)

Source: `internal/cmddesc/interpreter.go`'s `interpreters` map.

| Name                  | What it's for                                                                                                                                                                           | Where to find evidence                                                                                          |
| --------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `""` (empty, generic) | The default, flag-table-driven interpreter — used for every command whose semantics the generic `FlagSpecV1`/`PositionalSpecV1` table alone can express (this is almost every command). | The command's own `--help`/man page; no special interpreter-specific evidence needed.                           |
| `xargs`               | `xargs`'s own bespoke dispatch (builds a child invocation from its trailing command template).                                                                                          | `xargs --help`/`man xargs`; `internal/cmddesc/interpreter.go`'s `xargsInterpreter` for what it actually models. |
| `curl`                | `curl`'s data/`@file`/URL argument handling.                                                                                                                                            | `curl --help all` / `man curl`; `internal/cmddesc/interpreter.go`'s `curlInterpreter`.                          |
| `find`                | `find`'s `-exec`/`-execdir` child-invocation construction.                                                                                                                              | `find --help` / `man find`; `internal/cmddesc/interpreter.go`'s `findInterpreter`.                              |
| `kubectl`             | kubectl's own dispatch (context capture across subcommands).                                                                                                                            | `kubectl --help` / `kubectl <verb> --help`; `internal/cmddesc/interpreter_kubectl.go`.                          |
| `kubectl-manifest`    | kubectl subcommands that take a `-f`/manifest argument with `-` stdin special-casing.                                                                                                   | Same source, manifest-consuming verbs (`apply`, `create`, ...).                                                 |
| `kubectl-cp`          | `kubectl cp`'s local/remote positional split.                                                                                                                                           | `kubectl cp --help`; `internal/cmddesc/interpreter_kubectl.go`.                                                 |
| `ssh`                 | ssh's host-operand-as-`EffectNet` dispatch, with the remainder becoming a shell-dialect child invocation tagged REMOTE.                                                                 | `ssh --help` / `man ssh` (synopsis); `internal/cmddesc/interpreter_ssh.go`.                                     |
| `scp`                 | scp's per-operand local/remote classification, one `EffectNet` per distinct remote host.                                                                                                | `scp --help` / `man scp`; `internal/cmddesc/interpreter_scp.go`.                                                |

## Program dialects (4)

Source: `internal/cmddesc/dialect.go`'s `dialects` map. A dialect is what a `KindProgram`
(`"program"`) `OperandRoleV1`'s `Dialect` field names — the language the PROGRAM TEXT (not the
outer command) is written in.

| Name    | What it's for                                                                                                                                                                                                                                                                                                         | Where to find evidence                                                                   |
| ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `sed`   | A `sed` script operand (e.g. `xargs -I{} sed '<script>' {}`, or a command whose operand IS a sed program).                                                                                                                                                                                                            | `sed --help` / `man sed`; the script text itself.                                        |
| `awk`   | An `awk` program operand.                                                                                                                                                                                                                                                                                             | `awk --help` / `man awk`(1); the program text itself.                                    |
| `shell` | A shell command-line operand (parsed and recursed into as a child invocation).                                                                                                                                                                                                                                        | The operand's own text; `man sh`(1) for POSIX shell grammar if a construct is ambiguous. |
| `bash`  | Same as `shell` — `internal/cmddesc/dialect.go`'s own comment: `"shell"` and `"bash"` name the SAME interpreter (`shellDialect{}`); use whichever spelling matches how the citing evidence names it (a `bash -c` invocation → `"bash"`; a POSIX `sh -c` invocation → `"shell"`), not because they behave differently. | `bash --help` / `man bash`.                                                              |

**Known deliberate gap (do not try to "fix" this from this skill):** `cmddesc/registry.go`'s
`bashSchema` (and its `sh` alias) names a `"shell-file"` dialect for the case where `bash`/`sh` is
invoked with no `-c` (the first positional is a script FILE, not inline program text) —
`"shell-file"` is intentionally never registered in `dialects`, so `specfmt.Validate` rejects it as
unknown. This is a pre-existing, already-documented collision (see
`internal/embeddedspecs/dialects_test.go`'s own doc comment and `internal/speclint/lint.go`'s
`LintInvalid` doc comment) — `internal/cmddesc` and `internal/specfmt` are both out of this skill's
Files scope, so this skill does not resolve it; a spec you author for a script-FILE invocation of
`bash`/`sh` will surface as an `InvalidSpec`/WARN finding for that one reason, which is expected
and not this skill's bug to fix.
