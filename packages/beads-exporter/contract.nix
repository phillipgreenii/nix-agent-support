{
  lib,
  go,
  buildGoApplication,
  writeShellApplication,
  coreutils,
  bash,
}:

# The bd contract suite as a runnable package output.
#
#   beads-exporter-contract --bd <absolute path of a bd binary>
#
# The suite (build tag `contract`) creates a throwaway embedded database with a
# per-run unique prefix, fails unless that database is truly embedded, and pins
# the bd behaviour the exporter relies on. With `--update` it re-records the
# fixtures and the VERSION pin; point `--testdata` at the checkout's
# packages/beads-exporter/testdata/bd directory for that.
#
# It is compiled into a test binary because the suite is ordinary `go test`
# code; the wrapper only supplies the committed queue definitions and a
# hermetic PATH. The suite's bd children run with the same bash+coreutils PATH
# the real exporter's childPath carries (git deliberately absent), so a bd that
# is a shell wrapper can be pointed at with --bd.
let
  testBinary = buildGoApplication {
    pname = "beads-exporter-contract-test";
    version = "0.0.0";
    src = lib.cleanSource ./.;
    pwd = ./.;
    modules = ./gomod2nix.toml;
    inherit go;
    disableGoCache = true;
    doCheck = false;
    buildPhase = ''
      runHook preBuild
      export HOME="$TMPDIR" GOCACHE="$TMPDIR/go-build"
      go test -c -tags contract -o beads-exporter-contract.test ./internal/contract
      runHook postBuild
    '';
    installPhase = ''
      runHook preInstall
      mkdir -p "$out/libexec"
      install -m 0755 beads-exporter-contract.test "$out/libexec/"
      runHook postInstall
    '';
  };

  # The committed mirror the exporter's queue configuration is checked against.
  queues = ../../claude-marketplace/pb/queues.json;
in
writeShellApplication {
  name = "beads-exporter-contract";
  runtimeInputs = [
    coreutils
    bash
  ];
  text = ''
    exec ${testBinary}/libexec/beads-exporter-contract.test \
      -test.v -test.timeout=0 -queues ${queues} \
      -child-path ${
        lib.makeBinPath [
          bash
          coreutils
        ]
      } "$@"
  '';
  meta = {
    description = "bd contract suite for beads-exporter: pins the bd flags, output shapes and ready/list semantics against a real bd";
    mainProgram = "beads-exporter-contract";
  };
}
