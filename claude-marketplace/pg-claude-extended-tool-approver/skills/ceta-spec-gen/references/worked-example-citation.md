# Worked example: `jq`, `--from-file`

This is the one fully worked citation example `ceta-spec-gen`'s `SKILL.md` points to: one command,
one flag, the evidence read, the resulting `Citation`, and the resulting `Role`. Use it as a
template to pattern-match against, not as a claim that `jq`'s existing embedded spec (`internal/
embeddedspecs/data/jq.json`) already carries real citations — today it carries the thin,
interim `registry.go`/`DefaultRegistry()` placeholder on every field (Phase 1.2's mechanical
marshalling); this worked example shows what a REAL citation for the same flag looks like once
regenerated.

## 1. Evidence read

```
$ jq --help
Usage: jq [OPTIONS] FILTER [FILES...]
...
    -f, --from-file FILE      read filter from FILE instead of arguments;
...
```

(`jq --version` reported `jq-1.8.2` for provenance.)

## 2. What the excerpt tells us

- `-f`/`--from-file` takes exactly one argument (`FILE`).
- That argument is a path jq **reads** (the filter program's source), not a literal string and not
  a program-in-a-dialect jq itself interprets (jq's OWN filter language is not one of this
  repo's 4 registered `Dialect`s — sed/awk/shell/bash — so this operand is a plain path-read, not a
  `program`-kind role naming a `Dialect`).
- It's a normal named flag, not itself danger-shaped by this skill's `dangerPatterns` list (no
  match against `-o`, `--output*`, `--exec*`, `-c`, `--config*`, `--command`, `-e`, `--*-program`,
  `--*-hook*`, `--receive-pack`, `--upload-pack`, `--prune`, `--mirror`, `--all`, `--delete`, `-f`
  — wait, `-f` IS in `dangerPatterns` (`internal/speclint/lint.go`'s list includes bare `-f`) — see
  step 4 below for how that gets resolved, not skipped.

## 3. The resulting `Citation`

```json
{
  "source": "jq --help (jq-1.8.2): \"-f, --from-file FILE      read filter from FILE instead of arguments\""
}
```

Contrast with the thin placeholder this same field carries in the pre-existing embedded spec today
(`internal/embeddedspecs/data/jq.json`):

```json
{
  "source": "internal/cmddesc/registry.go: cmddesc.DefaultRegistry()[\"jq\"]"
}
```

The placeholder names WHERE the field lives in Go source (`internal/speclint`'s `isThinCitation`
matches on the literal substrings `"registry.go"` and `"DefaultRegistry()"` both being present);
the real one quotes the actual `--help` evidence that justifies the flag's arity and role.

## 4. The resulting `Role` (and the danger-flag check)

```json
{
  "-f": {
    "arity": "one",
    "operand": { "kind": "path-read" },
    "transform": { "kind": "none" },
    "citation": {
      "source": "jq --help (jq-1.8.2): \"-f, --from-file FILE      read filter from FILE instead of arguments\""
    }
  }
}
```

`kind: "path-read"` is one of `cmddesc.RoleKind.String()`'s spellings (`internal/cmddesc/
schema.go`) — an effect role (`isEffectRole` in `internal/speclint/lint.go` treats anything other
than `""`/`"literal"`/`"unmodeled"` as an effect role). Because `-f` IS danger-shaped
(`dangerPatterns` matches bare `-f`) AND takes a value, `internal/speclint`'s
`dangerFlagFindings` check requires every one of its operand roles to be an effect role — which
`path-read` satisfies, so this flag produces **no** `CheckDangerFlagRole` finding. (Had `-f` been
modeled as `"literal"` instead, the check would fire: a value-taking, danger-shaped flag with a
non-effect role is exactly the "someone modeled a real read as inert" case the check exists to
catch.)

The same jq spec's real embedded data (`internal/embeddedspecs/data/jq.json`) independently
confirms this exact role choice was already made once (`"-f": {"operand": {"kind":
"path-read"}, ...}`) — this worked example's job is showing the CITATION methodology, not
inventing a new role for a flag whose role this repo had already gotten right.
