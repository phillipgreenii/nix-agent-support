# phillipgreenii-nix-agent-support

Agent and AI tooling for macOS and Linux, packaged as a standalone, composable Nix flake. It is
meant to be imported by a machine or organization flake rather than used directly: the flake
exposes modules and packages, never a `darwinConfigurations` or `nixosConfigurations` entry.

The flake has no dependency on any private flake. Anything specific to one machine or one
organization is supplied by the consuming flake through module options and runtime
configuration files, not hardcoded here.

## What it provides

- **Claude Code setup** (`home/programs/claude-*`): settings, status line, hook router, a
  nix-built plugin marketplace (`claude-marketplace/`), and the tool approver
  (`packages/claude-extended-tool-approver`).
- **Agent work orchestration**: `pg-router` (event router and scheduler), `ccpool` (session
  pool), `pb` (beads workflow tool), `pg-connector` (issue and pull-request connectors),
  `pg-desk` and `pg-task-focus` (operator views).
- **Monitoring and housekeeping daemons**: `pa-monitor`, `pg-keep-awake`, `pg-rescue`,
  `beads-exporter`, `tmp-sweep`.
- **Small CLI tools** (`packages/`): `wtnew`, `wtdone`, `bg-tools`, `handoff-create`,
  `work-report`, and others. `ls packages` is the authoritative list.

## Flake outputs

| Output                         | Contents                                                                             |
| ------------------------------ | ------------------------------------------------------------------------------------ |
| `homeModules.default`          | Home Manager modules from `home/`, one directory per program under `home/programs/`  |
| `homeModules.capabilities`     | Capability definitions that switch groups of programs on together                    |
| `homeModules.install-metadata` | Records this flake's revision in the installed system                                |
| `darwinModules.default`        | nix-darwin modules from `darwin/` (system-level wiring such as launchd and alerting) |
| `nixosModules.default`         | NixOS modules from `nixos/`                                                          |
| `overlays.default`             | Overlay that adds this flake's packages to `pkgs`                                    |
| `packages.<system>`            | The packages under `packages/`                                                       |
| `checks.<system>`              | Unit, eval and integration checks                                                    |

All options live under the `phillipgreenii.*` namespace and each program module defines its own
options next to its configuration.

## Using it

Add the flake as an input and import the modules you want:

```nix
{
  inputs.phillipgreenii-nix-agent-support.url = "github:phillipgreenii/nix-agent-support";

  # in a Home Manager module:
  #   imports = [ inputs.phillipgreenii-nix-agent-support.homeModules.default ];
  #   phillipgreenii.programs.claude-code.enable = true;
  #
  # in a nix-darwin module:
  #   imports = [ inputs.phillipgreenii-nix-agent-support.darwinModules.default ];
  #   system.primaryUser = "<your user>";
}
```

The darwin modules install their per-user packages for `system.primaryUser`, so set it.

## Development

- Format with `nix fmt`.
- Build the check relevant to your change, for example `nix build .#checks.<system>.<name>`. A
  full `nix flake check` is not a routine gate.
- Hooks run from a per-clone bundle through `pg-hooks`; see `CLAUDE.md`.

## More documentation

- [`CLAUDE.md`](CLAUDE.md): repository rules, module design pattern and development workflow.
- [`docs/adr/index.md`](docs/adr/index.md): architecture decision records.
- `packages/<name>/README.md`: per-package documentation.
