{
  config,
  lib,
  options,
  ...
}:

# build_tmp (bead pg2-2qirl, packet A): a shared, auto-cleaned temp/build root on
# a machine-specific volume. This module creates the directory, exports the Go
# build/test temp variables to it from interactive shells (guarded by the
# directory's existence), and tells CETA the root is temporary AND read-write.
# The cleanup (registry items, sweep library) is packet B, the schedule is packet
# C, and enabling it for a host is packet D. Shared plumbing: lib/tmp-root.nix.
# Facts and tested exclusions: docs/guides/build-tmp-roots.md.

(import ../../../lib/tmp-root.nix { inherit lib; }).mkModule {
  name = "build-tmp";
  basename = "build_tmp";
  marker = ".build-tmp-root";
  activationName = "buildTmpRoot";
  summary = "temp/build output of Go builds and tests, swept when idle";

  # GOTMPDIR and pg-test-runner's root cover the Go build/test churn without the
  # general-TMPDIR risks (cross-volume EXDEV, case sensitivity, a long-lived
  # shared directory slowing a direct `go test`); TMPDIR is opt-in.
  envVars =
    cfg:
    [
      "GOTMPDIR"
      "PG_TEST_RUNNER_TMP_ROOT"
    ]
    ++ lib.optional cfg.exportTmpdir "TMPDIR";

  extraOptions.exportTmpdir = lib.mkOption {
    type = lib.types.bool;
    default = false;
    description = ''
      Also export `TMPDIR` to the root. Off by default: a general `TMPDIR` on
      another volume can break tools that rename from it into the home volume
      (EXDEV), and the volume may be case-sensitive; `GOTMPDIR` plus
      `PG_TEST_RUNNER_TMP_ROOT` already cover the Go build/test churn.
    '';
  };

  level1Default = 1440;
  level3Default = 60;
  level1Doc = "Default 1440 (24 h): finished temp work, nothing to regenerate.";
  level3Doc = "Default 60 (1 h): build output is regenerable locally but a refill costs recompilation.";
} { inherit config options; }
