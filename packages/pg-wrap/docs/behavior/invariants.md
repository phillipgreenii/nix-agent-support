# Invariants — pg-wrap

Rules this set's implementation MUST hold, following the behavior-docs method
(`phillipgreenii-nix-agent-support · behavior-docs/docs/behavior`). Names are namespaced by topic
(`INV-SPEC-1`). Because pg-wrap is a standalone tool that names no caller (`INV-ISO-1`), every
clause here is stated at pg-wrap's own floor: it speaks only of "a caller", "a launcher slot" and
"a payload".

## The wrap spec (`INV-SPEC-*`)

- **`INV-SPEC-1`** <!-- uuid: 3a9c7e05-d4b2-4186-bf63-58e1a0d2c7f9 --> — **A wrap spec is a TOML
  file with a fixed set of keys.** The keys are `schema_version` (an integer, required), `wrap`,
  `strict_env`, `keep` and `exec`. Any unknown key, any wrong type, and a missing `schema_version`
  is a decode error: pg-wrap MUST NOT guess, ignore or default its way past one. The spec is
  pg-wrap's own configuration, separate from any caller's; a caller names a spec to pg-wrap and
  pg-wrap alone reads it.
- **`INV-SPEC-2`** <!-- uuid: b7248d1e-0a3f-4c95-a6d8-9e5f13b02c47 --> — **A spec newer than the
  running pg-wrap is refused.** When `schema_version` is greater than the highest version the
  running pg-wrap supports, pg-wrap MUST refuse it with exactly
  `pg-wrap[spec]: <path> has schema_version N, this pg-wrap supports <= M.`, where `<path>` is the
  spec's path, `N` the spec's version and `M` the highest supported.
- **`INV-SPEC-3`** <!-- uuid: 6f01c3b9-e8a5-47d2-9014-d7b3a6e25f88 --> — **`wrap` is an ordered
  list, outermost first, of entries in two forms.** A **string entry** is shell-parsed, so `$VAR`
  expands inside the previous wrapper's environment. An **array entry** is argv form: executed
  directly with the rest appended, never shell-parsed. A spec has no templating and no per-item
  context; per-item values reach a wrapper through its caller (for example as argv), never through
  the spec (`INV-WC-8`).
- **`INV-SPEC-4`** <!-- uuid: c45a9e72-31f6-4b08-92ce-8a07d5f1b3e6 --> — **`strict_env` and `keep`
  choose the starting environment.** `strict_env` (boolean, default false) selects the minimal
  environment instead of the inherited one (`INV-ENV-1`). `keep` lists variable names that are
  also kept, by EXACT name and never by prefix: a prefix rule would drop names that merely share a
  stem with a kept one. An example of what `keep` is for is the markers a payload's launcher
  injects.
- **`INV-SPEC-5`** <!-- uuid: 18e7d3a4-b5c9-4f60-a2d1-6c94e0b7f352 --> — **`exec` is an optional
  argv prefix whose first element is an absolute path.** It lets a launcher slot that takes only a
  program path run the real payload: the payload is `exec` followed by the args pg-wrap was given,
  in both `run` and `pg-wrap-exec`. When `exec` is absent the payload is the args alone.
- **`INV-SPEC-6`** <!-- uuid: f9b2c6d0-4a17-4e83-8f5b-1d3e7a90c264 --> — **Paths are absolute.**
  The first word of every string entry, the first element of every array entry and the first
  element of `exec` MUST be an absolute path. Helpers that render specs from known store paths are
  a convenience for an author; any author may write a spec by hand, and pg-wrap MUST NOT depend on
  how a spec was authored (`INV-ISO-1`).

## Entry points (`INV-ENTRY-*`)

- **`INV-ENTRY-1`** <!-- uuid: 7d3f08a5-c1e6-4927-b4a8-e25c9d6f1a03 --> — **`pg-wrap run --spec
<file> -- <cmd> [args…]`** reads the spec, builds the environment (`INV-ENV-1`), applies the
  layers (`INV-COMP-1`) and execs. The payload is `<cmd> [args…]`, preceded by the exec prefix when
  the spec has one (`INV-SPEC-5`).
- **`INV-ENTRY-2`** <!-- uuid: 2e65b9f1-a083-4d7c-9b32-f8c01e4a7d65 --> — **`pg-wrap-exec [args…]`
  is a second, separate binary.** It is for a caller that lets you replace only a program path. It
  takes the spec path from the spec-path variable and execs the spec's exec prefix followed by ALL
  the args it was given; a leading `run` or `check` is just an arg. It is a second binary rather
  than a dispatch on the program's own name, because a launcher script placed in front of a program
  would break a name-based dispatch.
- **`INV-ENTRY-3`** <!-- uuid: d08a4c37-5e92-4b1f-a7c6-3b9f02d8e514 --> — **`pg-wrap check --spec
<file>` is pg-wrap's ONE validation entry point.** It decodes the spec strictly (`INV-SPEC-1`,
  `INV-SPEC-2`) and verifies that every referenced path (`INV-SPEC-6`) is an executable regular
  file: a directory, a broken symlink or a non-executable file is a problem. It prints one line
  per problem and exits non-zero (97, `INV-FAIL-1`) if there are any. A build that renders a spec
  runs `check` on it.
- **`INV-ENTRY-4`** <!-- uuid: 4b71e0c9-f2d3-48a5-8e07-a6c3d95b1f20 --> — **`pg-wrap show --spec
<file>` prints the composed layers** the spec would run, without running anything.
- **`INV-ENTRY-5`** <!-- uuid: a3c5f8e2-0b64-4d19-b8f7-71e2a9c0d438 --> — **The spec-path variable
  never reaches the payload.** pg-wrap MUST remove it from the payload's environment in every mode
  (`run`, `pg-wrap-exec`, `check`, `show`), so a payload that itself uses pg-wrap is not pointed at
  its caller's spec.
- **`INV-ENTRY-6`** <!-- uuid: f7f31d28-59e3-4262-964c-bd69b773f771 --> — **`pg-wrap show --spec
<file> --env-from <dump>` lists which names the spec's environment would keep and which it would
  drop.** The dump is a text file of `NAME=value` lines, one per variable; blank lines are skipped
  and every value is ignored. For each distinct name in the dump, pg-wrap reports whether the
  starting environment of `INV-ENV-1` would carry it: with `strict_env` false every name is kept
  except the spec-path variable; with `strict_env` true a name is kept only when it is one of the
  minimal environment's names or is listed in `keep`, matched by EXACT name (`INV-SPEC-4`), so a
  name that merely shares a stem with a kept one is dropped. The spec-path variable is dropped in
  both cases (`INV-ENTRY-5`), and `PATH` is dropped under `strict_env` because the minimal
  environment sets its own fixed `PATH`. The output is names only: pg-wrap MUST NOT print, log or
  quote any value from the dump, in its listing or in any message, and nothing is run. The listing
  follows the layers `show` prints (`INV-ENTRY-4`). A dump that cannot be read, or that has a line
  that is not `NAME=value`, is a failure (`INV-FAIL-1`) at the `env` stage; the message names the
  file and the line number and never the line's text. This is how a spec author finds a `keep`
  entry the spec is missing.

## Composition (`INV-COMP-*`)

- **`INV-COMP-1`** <!-- uuid: 85f2d6b0-9c1a-4e47-93d5-0a7b8e3c6f19 --> — **One layer per entry.**
  For a string entry, layer _i_ is `/bin/sh -c 'exec <wrap_i> "$@"' sh <layer i+1…>`. For an array
  entry, layer _i_ is its elements followed by `<layer i+1…>`. The innermost rest is the payload
  argv.
- **`INV-COMP-2`** <!-- uuid: 1c9e47a3-d6f8-4b52-a031-e9d5c2b7084a --> — **Each layer sees the
  environment the previous layer left.** Because a layer is its own shell, an environment edit in
  one entry takes effect before the next entry runs. Example (measured): two nested wraps that each
  prepend a directory to `PATH` gave `/opt/b:/usr/bin:/bin` when both sat in ONE shell command
  (which expanded `$PATH` once, before either wrap ran), and `/opt/b:/opt/a:/usr/bin:/bin` as
  per-entry layers, where each edit built on the one before. Payload args arrive as `"$@"` and are never parsed as shell. The shell is POSIX `/bin/sh`
  and reads no rc files.

## Environment (`INV-ENV-*`)

- **`INV-ENV-1`** <!-- uuid: e7a0b5d4-23c1-4f86-8c9a-5d1f6e3b72c0 --> — **The starting environment
  is inherited, or minimal.** With `strict_env` false the layers start from the inherited
  environment. With `strict_env` true they start from the minimal environment: `HOME`, `USER`,
  `LOGNAME`, `TMPDIR`, `LANG`, `TERM` and `COLORTERM` where set, and
  `PATH=/usr/bin:/bin:/usr/sbin:/sbin`, plus the names in `keep` (`INV-SPEC-4`) that are set. Every
  other inherited variable is dropped. In both cases the spec-path variable is removed
  (`INV-ENTRY-5`).

## Failures (`INV-FAIL-*`)

- **`INV-FAIL-1`** <!-- uuid: 36d8c1f9-7a4e-4b03-b5e2-c8a90d4f1b67 --> — **Every failure pg-wrap
  detects exits before the payload with code 97.** That covers a spec decode error, `check`
  problems, an unset spec-path variable and an `exec` of a missing layer. 97 is none of 1, 9, 124
  or 125 to 127, so a caller can tell pg-wrap's own failure from the payload's status and from
  common launcher codes.
- **`INV-FAIL-2`** <!-- uuid: 9f4b2e68-c5d0-4a71-96f3-2e8d1a7c5b04 --> — **Messages name their
  stage.** Every message pg-wrap writes has the form `pg-wrap[<stage>]: <reason>`, where `<stage>`
  is one of `spec`, `env`, `layer <i>` and `exec`.
- **`INV-FAIL-3`** <!-- uuid: 0b6c9d3a-e1f7-4528-a4b0-d73e5f8a2c91 --> — **`pg-wrap-exec` with no
  spec says why.** When the spec-path variable is unset it fails (`INV-FAIL-1`) with the reason
  "PG_WRAP_SPEC is not set in the environment this launcher received; a terminal-multiplexer pane
  does not inherit your shell's variables".

## Isolation (`INV-ISO-*`)

- **`INV-ISO-1`** <!-- uuid: c2e18f75-4b9a-4d36-8a0c-6f5b3d1e9a72 --> — **pg-wrap names no caller
  and validates none.** pg-wrap and whatever invokes it are mutually unaware: neither imports,
  names or validates the other, and pg-wrap's behavior MUST NOT vary with what the payload or the
  caller is. Its documentation says only "a caller", "a launcher slot" and "a payload".

## The wrapper contract (`INV-WC-*`)

Every layer a spec names obeys these, and so does pg-wrap acting as a layer.

- **`INV-WC-1`** <!-- uuid: 57a3d0e4-b8c6-4f19-92e7-a1c4f6b05d38 --> — A wrapper MUST run the rest
  of its argv as the payload, with argv and stdin unmodified. (WC-1)
- **`INV-WC-2`** <!-- uuid: d9f6a1c2-30e5-47b8-b1d4-8c2e7a9f3065 --> — A wrapper MUST return the
  payload's exit status unchanged, and MAY return its own non-zero status only for its own failure.
  On activation failure it MUST exit before starting the payload, with the reason on stderr. It
  SHOULD NOT exit 9, which some callers read as "busy, retry later". pg-wrap cannot remap a layer's
  exit, because layers exec. (WC-2)
- **`INV-WC-3`** <!-- uuid: 41c7e9b8-a2d3-4065-8f1a-e5b0d6c3a972 --> — A wrapper MUST NOT write to
  stdout. It MAY write to stderr, and SHOULD prefix its own messages with its stage. (WC-3)
- **`INV-WC-4`** <!-- uuid: ab5d3f06-6c18-4e92-a7b9-0d4e2f8c1b53 --> — A wrapper MUST leave the
  payload with the same controlling terminal, in the foreground process group. (WC-4)
- **`INV-WC-5`** <!-- uuid: 7e0c8a42-d9b5-4a3f-b6c1-93f7e1d5a208 --> — A wrapper MUST deliver
  SIGTERM, SIGINT and SIGHUP to the payload, then clean up and exit (exec'ing satisfies this).
  (WC-5)
- **`INV-WC-6`** <!-- uuid: f3a8b1d7-5e40-4c26-9d8b-b2a6c0e47f19 --> — A wrapper MUST work under the
  minimal environment alone, reaching its tools by absolute path, and MUST NOT prompt. (WC-6)
- **`INV-WC-7`** <!-- uuid: 28d4e6c0-f7a1-4b95-8e3c-4a9d1b7f6e52 --> — A wrapper SHOULD exec when it
  has no cleanup to do. (WC-7)
- **`INV-WC-8`** <!-- uuid: 6b9f2a15-c3e8-4d70-a5f4-d18c07e3b9a6 --> — A wrapper MUST get per-item
  data only from env vars or argv, never from text interpolated into a string entry. (WC-8)
- **`INV-WC-9`** <!-- uuid: e0d7c4b3-9a62-4f18-b9e5-3c5a8f21d074 --> — First words and first
  elements MUST be absolute paths, checked by `check` and at every run. (WC-9)
