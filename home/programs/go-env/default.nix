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
# GOFLAGS=-p=<parallelism> -trimpath. Without a bound, every direct `go` command uses
# -p <cores>, and a burst of concurrent agent sessions drove the host to a load
# average near 100. An environment GOFLAGS REPLACES the file value, so a script
# that sets GOFLAGS must append to the existing value. The value is built by
# ./goflags.nix, which the pg-router daemon's launchd environment also imports,
# so the two cannot drift.
#
# -trimpath (option `trimpath`, default true). Without it Go hashes each
# non-GOROOT package's absolute directory into its compile action key, so every
# worktree, workforest checkout and per-task worktree compiles its own copy of
# every package it touches into the shared GOCACHE (a 2026-11-20 sample found
# 78% of cache bytes embedding a worktree path; entries of removed worktrees can
# never be hit again). With -trimpath the key records module path@version
# instead, so two checkouts of the same source share every entry. The ZR
# monorepo already sets GOFLAGS=-trimpath in its own environment, which replaces
# this file, so it is unaffected. Overrides: `-trimpath=false` on a command line
# overrides GOFLAGS; an empty `GOFLAGS=` in the environment does NOT override the
# file (only a non-empty value replaces it). Known costs: delve file breakpoints
# need substitute-path (or build with -trimpath=false); panics and stack traces
# show import-path-relative file paths; `go tool pprof -list` needs -source_path;
# runtime.GOROOT() is empty in a -trimpath binary unless GOROOT is set; test
# results (cached) now carry over between worktrees of the same source, since
# the test binary is identical (files a test opens are still tracked by absolute
# path; files outside the module root are not); debug.BuildInfo drops -ldflags
# and CGO_* and records -trimpath=true; after the first apply every cache key
# changes once, so the cache briefly grows until old entries age out (5 unused
# days).
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
  goflags = import ./goflags.nix;
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
      default = goflags.defaultParallelism;
      description = "Value of the `-p` go build flag (packages built or tested in parallel).";
    };

    trimpath = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Append `-trimpath` to GOFLAGS so the build cache keys do not embed each
        checkout's absolute path and worktrees share cache entries. See the
        module header for overrides and known costs.
      '';
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
      GOFLAGS = goflags.mkGoflags {
        inherit (cfg) parallelism trimpath;
      };
    }
    // lib.optionalAttrs (cfg.toolchain != null) {
      GOTOOLCHAIN = cfg.toolchain;
    };
  };
}
