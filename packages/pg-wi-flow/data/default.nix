{ pkgs }:
# pg-wi-flow's paths.defaults store directory (design: ## Configuration,
# "paths" -> "defaults"). Copies the WHOLE data tree (README.md, stages/,
# concerns/, checklists/) so any file added under it reaches the store path
# with no further edit here. A plain pkgs.runCommand copy rather than
# mkBashLibrary/mkBashScript: this derivation has no bash entry point of its
# own, just a directory of static data files for lib/context.bash's
# `paths.defaults` fallback to read.
pkgs.runCommand "pg-wi-flow-data" { } ''
  mkdir -p "$out"
  cp -r ${./.}/. "$out/"
  chmod -R u+w "$out"
  rm -f "$out/default.nix"
''
