{ config, lib, ... }:

# go-env: a default Go build parallelism for every context, via the Go env file.
#
# Without a bound, every direct `go` command uses -p <cores>, and a burst of
# concurrent agent sessions drove the host to a load average near 100. The value
# is written through home-manager's `programs.go.env`, which renders Go's own
# env file (`go env GOENV`: ~/Library/Application Support/go/env on darwin,
# $XDG_CONFIG_HOME/go/env elsewhere). Go reads that file in every context, including
# tmux sessions started by launchd, git hooks and `pn workspace build`, which
# `home.sessionVariables` does not reach. The file is only written when
# `programs.go.enable` is on (nix-personal's go module enables it).
#
# An environment GOFLAGS REPLACES the file value, so a script that sets GOFLAGS
# must append to the existing value. Nix sandboxes set HOME=$TMPDIR and keep
# their own -p, so this does not touch them.

let
  cfg = config.phillipgreenii.programs.go-env;
in
{
  options.phillipgreenii.programs.go-env = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Write GOFLAGS=-p=<parallelism> into the Go env file (via `programs.go.env`).";
    };

    parallelism = lib.mkOption {
      type = lib.types.ints.positive;
      default = 3;
      description = "Value of the `-p` go build flag (packages built or tested in parallel).";
    };
  };

  config = lib.mkIf cfg.enable {
    programs.go.env.GOFLAGS = "-p=${toString cfg.parallelism}";
  };
}
