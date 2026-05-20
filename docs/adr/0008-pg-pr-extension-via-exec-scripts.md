# pg-pr extension via exec scripts

**Status**: Accepted
**Date**: 2026-05-20
**Deciders**: Phillip Green II

## Context

ZR-specific providers (Captain's Log CICD, ZR-flavored jira) cannot live in the org-agnostic `phillipgreenii-nix-agent-support`. We need an extension mechanism that lets `phillipg-nix-ziprecruiter` ship adapters without rebuilding `pg-pr` core. Extension binaries should be Go programs (not bash scripts) so they can reuse `pg-pr`'s `pkg/` library and stay type-safe.

## Decision

Two extension surfaces:

1. **Provider script-out**: config references a provider as `exec:<binary-name>`. `pg-pr` spawns the binary, sends a JSON request on stdin, reads a JSON response on stdout. Protocol types are exported from `pg-pr`'s `pkg/api/`. Helper functions in `pkg/plugin/scriptout/` let extension authors write a single `func main() { scriptout.ServeCICD(&impl{}) }` and get a conformant binary.

2. **Command plugins (kubectl-style)**: `pg-pr <unknown>` searches PATH for `pg-pr-<unknown>` and execs it with the same env and argv. Used for future workflow commands like `pg-pr zr-rollback`.

Extension binaries are Go programs that import `github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg` as a library. They do not rebuild `pg-pr` core.

## Consequences

### Positive

- No cross-repo build coupling.
- Org-specific code stays in org-specific flakes.
- Composite providers possible (a repo can list multiple CICD providers; `pg-pr` fans out and merges).
- Type-safe: extensions get Go interfaces from `pkg/`.

### Negative

- Subprocess latency per provider call. Acceptable because provider calls are bounded (sync runs every N minutes; not per-keystroke).
- The script-out protocol is a versioned API the core cannot break unilaterally.

### Neutral

- Two distinct extension surfaces (provider script-out + command plugins) to document and maintain.

## Alternatives Considered

### Go compile-time `plugin` package

Rejected. Go plugins are fragile on Darwin, version-locked to the host binary, and not commonly used. They would tie extension authors to the exact Go toolchain and pg-pr version used to build core.

### Library-link nix-ziprecruiter's binaries against the same pg-pr source

Rejected. The user explicitly requested that extensions not rebuild core. Library-link forces every extension to track core's exact source state.

### All-runtime (every adapter is a script, even builtins)

Rejected. Adds subprocess latency to every call including the common GitHub path. Fights the existing Go code we are absorbing.

## Related Decisions

- [0007-pg-pr-go-cli-consolidation.md](0007-pg-pr-go-cli-consolidation.md)
- [0009-pg-pr-bead-schema.md](0009-pg-pr-bead-schema.md)

See also: `docs/superpowers/specs/2026-05-19-pg-pr-design.md` §"Provider extension protocol".
