{ pkgs }:
# pg-wi-flow's paths.defaults store directory (bead tc-9ddu3.1.5, design: ##
# Configuration, "paths" -> "defaults"). This phase ships only the
# Data-file-contract README (see ./README.md) -- no stages/, concerns/, or
# checklists/ files, since the null workflow (the only workflow this phase
# configures) needs none; homelab's real data-overlay files land in
# execution phase 2. A plain pkgs.runCommand copy rather than mkBashLibrary/
# mkBashScript: this derivation has no bash entry point of its own, just a
# directory of static data files for lib/context.bash's `paths.defaults`
# fallback to read.
pkgs.runCommand "pg-wi-flow-data" { } ''
  mkdir -p "$out"
  cp ${./README.md} "$out/README.md"
''
