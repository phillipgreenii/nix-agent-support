{ config, lib, ... }:

# go-env: default Go settings for every context, via the Go env file.
#
# Where the values go. They are written through home-manager's `programs.go.env`,
# which renders Go's own env file (`go env GOENV`: ~/Library/Application
# Support/go/env on darwin, $XDG_CONFIG_HOME/go/env elsewhere). Go reads that
# file in every context, including tmux sessions started by launchd, git hooks
# and `pn workspace build`, which `home.sessionVariables` does not reach. The
# file is only written when `programs.go.enable` is on (nix-personal's go module
# enables it).
#
# GOFLAGS=-p=<parallelism>. Without a bound, every direct `go` command uses
# -p <cores>, and a burst of concurrent agent sessions drove the host to a load
# average near 100. An environment GOFLAGS REPLACES the file value, so a script
# that sets GOFLAGS must append to the existing value.
#
# GOTOOLCHAIN=local. Go hashes the compiler's build ID into every compile action
# key, so each distinct compiler binary keeps its own full copy of everything it
# compiles in a shared GOCACHE. With Go's default (auto), a `go` older than a
# module's go directive silently downloads a golang.org/toolchain module and
# re-execs it, adding another compiler (and another copy of the cache) for every
# release it fetches. With local, a too-old Go fails instead with
# `go: go.mod requires go >= X (running go Y; GOTOOLCHAIN=local)`. Override
# rules: a NON-EMPTY environment GOTOOLCHAIN (for example GOTOOLCHAIN=auto for
# one command, or a tool that sets GOTOOLCHAIN in its own environment) wins over
# the file; an empty GOTOOLCHAIN= does not override it; `go env -w` cannot
# change it, because the file is a read-only nix-store symlink. Side effect:
# `go mod tidy` and `go get -u` in the update scripts now fail, rather than
# switching toolchains, when a dependency needs a newer Go than the one on PATH.
#
# Nix builds do not read this file: the nix builder runs them with
# HOME=/homeless-shelter (this host runs nix with sandbox = false, so it is HOME,
# not a sandbox, that keeps the file out), and mkGoTest/mkGoLint set
# HOME=$TMPDIR and keep their own -p.

let
  cfg = config.phillipgreenii.programs.go-env;
in
{
  options.phillipgreenii.programs.go-env = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Write go-env's settings (GOFLAGS, GOTOOLCHAIN) into the Go env file (via `programs.go.env`). False writes neither.";
    };

    parallelism = lib.mkOption {
      type = lib.types.ints.positive;
      default = 3;
      description = "Value of the `-p` go build flag (packages built or tested in parallel).";
    };

    toolchain = lib.mkOption {
      type = lib.types.nullOr lib.types.nonEmptyStr;
      default = "local";
      description = ''
        Value of GOTOOLCHAIN in the Go env file. The default `local` stops a `go`
        older than a module's go directive from downloading another toolchain
        (each extra compiler keeps its own copy of the shared build cache).
        `null` writes no GOTOOLCHAIN, leaving Go's own default.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    programs.go.env = {
      GOFLAGS = "-p=${toString cfg.parallelism}";
    }
    // lib.optionalAttrs (cfg.toolchain != null) {
      GOTOOLCHAIN = cfg.toolchain;
    };
  };
}
