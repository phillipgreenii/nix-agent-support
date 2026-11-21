# The one definition of the GOFLAGS value the workspace sets for Go builds.
#
# Two places render it and MUST agree: the go-env home module (the Go env file)
# and the pg-router daemon's launchd environment (darwin/modules/pg-router). An
# environment GOFLAGS replaces the env file value, so the launchd copy has to
# carry every flag the file does. Both import this function; the tests in
# tests/go-env-eval.nix and flake.nix (test-pg-router-module) pin the result.
{
  defaultParallelism = 3;

  # goflags { parallelism, trimpath } -> "-p=<n>" or "-p=<n> -trimpath"
  mkGoflags =
    {
      parallelism,
      trimpath,
    }:
    "-p=${toString parallelism}" + (if trimpath then " -trimpath" else "");
}
