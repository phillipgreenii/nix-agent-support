# Wiring Patterns

How to wire scripts into nix packages and home-manager modules. Read this when adding a new script to a module or creating a new module.

## Simple single-script package (support-apps pattern)

```nix
# packages/my-tool/my-tool/default.nix
{ mkBashScript, pkgs }:

mkBashScript {
  name = "my-tool";
  src = ./.;
  description = "Does the thing";
  runtimeDeps = [ pkgs.jq pkgs.git ];
}
```

```nix
# packages/my-tool/default.nix
{ pkgs, bashBuilders }:
let
  my-tool = pkgs.callPackage ./my-tool {
    inherit (bashBuilders) mkBashScript;
  };
in
{
  inherit my-tool;
  packages = my-tool.packages;
  checks = { test-my-tool = my-tool.check; };
  tldr = my-tool.tldr;
}
```

## Module with library + scripts (project pattern)

```nix
# modules/zm/scripts.nix
{ pkgs, bashBuilders, myproject-lib, globalWorktreePath, ... }:
let
  testSupport = ./test-support;

  zm-lib = pkgs.callPackage ./lib {
    inherit (bashBuilders) mkBashLibrary;
    inherit pkgs myproject-lib testSupport;
  };

  zm-cmd-one = pkgs.callPackage ./zm-cmd-one {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs zm-lib testSupport;
  };

  zm-helper = pkgs.callPackage ./zm-helper {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs zm-lib testSupport;
    inherit globalWorktreePath;  # config injected to mkBashScript
  };

  allScripts = [ zm-cmd-one zm-helper ];
in
{
  inherit zm-lib zm-cmd-one zm-helper;
  packages = builtins.concatLists (map (s: s.packages) allScripts);
  tldr = builtins.foldl' (acc: s: acc // s.tldr) { } allScripts;
  checks = {
    test-zm-lib = zm-lib.check;
    test-zm-cmd-one = zm-cmd-one.check;
    test-zm-helper = zm-helper.check;
  };
}
```

## Home-manager module consumption (thin wrapper)

```nix
# modules/zm/default.nix
{ config, lib, pkgs, ... }:
let cfg = config.phillipgreenii.programs.zm;
in
{
  options.phillipgreenii.programs.zm = {
    enable = lib.mkEnableOption "zm monorepo tools";
    scriptsPackage = lib.mkPackageOption pkgs "zm-scripts" { };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.scriptsPackage ];
  };
}
```

The `zm-scripts` package is built in the flake overlay (where `self` is available) and consumed here. **Never** instantiate `bashBuilders` inside a home-manager module — `self` isn't available.

## Reference migrations (real-world examples by complexity)

> Paths shown are illustrative from this repository; adapt to your own structure.

| Example              | Path                                            | Pattern                                                 |
| -------------------- | ----------------------------------------------- | ------------------------------------------------------- |
| Simplest             | `support-apps/packages/nix-tools/check-flakes/` | Single script, completions, tldr, tests                 |
| Library + scripts    | `support-apps/packages/claude-activity/`        | Shared lib + 3 scripts + wrapper pattern                |
| Flake overlay wiring | `personal/home/programs/cmux/scripts.nix`       | Build at overlay, consume in home-manager               |
| Config injection     | `your-flake/modules/your-module/`               | 11 scripts, library, JSON config, composition           |
| Most complex         | `your-flake/modules/your-complex-module/`       | 14 scripts, library, exported + local config, git hooks |

Every migration has been validated through `nix flake check`.
