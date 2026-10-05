{
  runCommand,
  bash,
  coreutils,
  gnugrep,
  gnused,
}:

# Reusable bd flag/version check.
#
# Builds an empty derivation that succeeds only when the given bd package
# (1) reports exactly the version recorded in testdata/bd/VERSION and
# (2) provides every flag recorded in testdata/bd/flags.txt.
#
# A downstream flake runs it against ANOTHER bd package by calling the function
# this file returns with that package:
#
#   pkgs.callPackage ./bd-flags-check.nix { } { bd = someOtherBd; }
#
# `dataDir` defaults to this package's recorded data; it MAY be overridden to
# check a bd against a different recording.
{
  bd,
  name ? "beads-exporter-bd-flags",
  dataDir ? ./testdata/bd,
}:

runCommand name
  {
    nativeBuildInputs = [
      bash
      coreutils
      gnugrep
      gnused
    ];
  }
  ''
    bash ${./check-bd-flags.sh} ${bd}/bin/bd ${dataDir}
    touch "$out"
  ''
