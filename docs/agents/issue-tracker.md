# Issue tracker: beads (`bd`)

Issues live in **beads**, reached through the `bd` CLI. There is no GitHub/GitLab
issue surface for this workspace.

**The contract itself is not in this file.** It is the `wayfinder-beads` skill, in
this repo's `claude-marketplace/wayfinder-beads/`. Invoke that skill for the `bd`
operation mapping, `/wayfinder`'s "Wayfinding operations" (map, child tickets,
blocking edges, claiming, and the frontier query), and the triage label vocabulary.

## Why the skill and not this file

Matt Pocock's engineering skills (`/wayfinder`, `/triage`, `/to-tickets`, `/to-spec`)
each read a per-repo `docs/agents/issue-tracker.md`, so this path is where they look.
But a per-repo file is the wrong home for the binding:

- it is **per repo**, and every repo in this workspace shares one beads database, so
  each copy is duplication that drifts;
- pointing at one repo's copy by absolute path binds the rule to a single checkout on
  a single machine, which is exactly what stops it being reusable elsewhere.

The skill has neither problem. It ships in the nix-built marketplace that
`homeModules` registers automatically (see `flake.nix`'s
`marketplaces.nixProvided`), so it is present on every machine importing this
flake's home-manager module — no path, no per-repo file, no per-machine setup — and
it is content-digest versioned per ADR 0017 like every other plugin here.

Rule **T-1** in `home/programs/agent-rules/pgii-agent-rules.md` is what routes a
skill to it, and **T-3** forbids reintroducing an absolute path.

## Do not regenerate this

`/setup-matt-pocock-skills` would overwrite this file with its GitHub-Issues
template (a GitHub `git remote` is its default posture). Do not run it; changing
trackers is an operator decision (**T-2**).
