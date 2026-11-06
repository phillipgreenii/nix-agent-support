# pg-review-provider-config-inventory notes

The provider reads memory, skills, instructions and settings from disk (the
recorder does not ingest them), keeps the layers that load in the scope, and
prints evidence rows (one JSON array on stdout, schema `evidence-rows`) for the
config-health lens. It computes only mechanical facts; the analyst judges
semantic conflicts and redundancy.

```text
pg-review-provider-config-inventory --since T --until T --scope NAME
    [--scope-prefix P]... [--layer L]... [--exclude-cwd-prefix P]...
    [--user-dir DIR]
```

Exit 0 on success, 2 on a usage error (missing flag, bound without an offset,
until before since, unknown layer, `project` layer without a `--scope-prefix`,
stray argument), 1 on unreadable input (a configuration file that cannot be
read, a settings file that is not valid JSON, a failing `pg-ccaudit` call);
nothing is written to stdout on failure. The runner passes `--scope-prefix`
and `--layer` from the scope's configuration; the provider reads no config
file. Its canned query name is `inventory` (the built-in provider entry lists
it for the config-health lens) and every row carries it.

## Layers (the names a scope's `layers` entry may list)

A layer is loaded only when the scope lists it; a layer not listed contributes
nothing (but a symlink from a loaded layer into it still resolves to it, see
`defined_in`). The scope root is the first `--scope-prefix`. The user
directory is `--user-dir`, else `$CLAUDE_CONFIG_DIR`, else `$HOME/.claude`.

| Layer     | Files read                                                                                                                                       |
| --------- | ------------------------------------------------------------------------------------------------------------------------------------------------ |
| `user`    | user dir: `CLAUDE.md`, `settings.json`, `skills/*/SKILL.md`, `memory/*.md`, and the per-project `projects/<slug of scope root>/memory/*.md`      |
| `rules`   | `*.md` directly under the user dir's `rules/` and under the scope root's `.claude/rules/`                                                        |
| `project` | scope root: `CLAUDE.md`, `AGENTS.md`, `.claude/settings.json`, `.claude/settings.local.json`, `.claude/skills/*/SKILL.md`, `.claude/memory/*.md` |

A skill's names are its directory name and its frontmatter `name`. A real file
reached through two paths (a symlink) is one entry.

## Subjects, `defined_in` and paths

`defined_in` is the layer of the RESOLVED real path (longest matching layer
root wins; a path under none is `external`, shown by base name only). The
subject records where a thing is defined, not where it was seen, so a user
skill symlinked into a project is the user's subject, and the same user skill
seen from two scopes is one subject key with two occurrences (one row per run,
carrying that run's scope). Paths in subjects and samples are `<layer>:<path
relative to that layer's root>` (rules add `user/` or `project/`); the
per-project memory directory is never named by its home-derived slug in a
path that is shown. Reference targets are shown as written when relative or
`~/`, relative to the scope root when absolute under it, otherwise
`<abs>/<base name>`. Skill rows are keyed `skill:<layer>:<name>`; other file
rows `<kind>:<layer>:<relative path>`.

## Checks and thresholds (each a deterministic script check)

- **stale-reference** (type `reference`, metric `missing`): in an
  instruction, memory or skill file, outside fenced code blocks, a markdown
  link target `](target)` or an `@`-import (`@~/`, `@./`, `@../`, `@/` forms
  only) that is local (no scheme, not an anchor, fragment and query dropped,
  trailing punctuation trimmed) and whose path does not exist. Relative
  targets resolve against the real file's directory, `~/` against `$HOME`.
  Also a skill directory or instruction file that is a dangling symlink
  (subject `link:...`, metric `dangling_symlink`).
- **conflict**: (type `setting`) one dotted settings key whose scalar value
  differs between two loaded settings files (arrays are skipped because
  layers merge them); samples show a hash of each value, never the value.
  (type `skill`) one skill name defined by two different real files with
  different normalised text. Semantic conflicts are left to the analyst.
- **duplicate** (type `text`, metrics `copies`, `bytes`): two or more
  different real files among instruction, memory and skill files with
  identical whitespace-normalised text of at least 40 bytes.
- **oversize**: lines over 200 for instruction and memory files, over 500 for
  a skill file, or more than 25000 bytes for any of them (settings are never
  oversize). Metrics `lines`, `bytes`, `limit_lines`, `limit_bytes`.
- **unused** (type `skill`, metrics `calls` = 0, `age_days`): the window is at
  least 7 days, the skill file's modification time is before the window
  start, and `pg-ccaudit query skill-calls` (passed `--cwd-prefix` for every
  scope prefix and every `--exclude-cwd-prefix`, bounds normalised to UTC)
  returned no call naming it. A call name matches a skill name exactly or as
  the part after a `plugin:` prefix. When the window is shorter or no skill is
  old enough the query is not run.

## Row shape

`id` is `E-001` onward over rows sorted by kind then subject key, so two runs
over the same tree give identical output. `first_seen` and `last_seen` are the
involved files' modification times (whole seconds, UTC), or the window bounds
when no file applies. `baseline` is empty; `samples` are at most 5, sorted.
