{
  pkgs,
  bashBuilders,
}:
let
  # pg-rescue's first consumer (bead pg2-zr3jf; design
  # docs/superpowers/specs/2026-10-02-pg-rescue-design.md section 11): rebase
  # every workspace repo under `pg-rescue --chain sync`, then `pn workspace
  # push`. Location decision: it lives HERE as its own packages/ entry (project
  # label `sync-projects`), not in phillipg-nix-repo-base next to pnwf. It is a
  # thin script over `pg-rescue` and `pn`, and `pg-rescue` -- the only thing it
  # exists to showcase -- is built and configured in this repo; the
  # home-manager module that installs it (home/programs/pg-rescue) is here too.
  sync-projects = pkgs.callPackage ./sync-projects {
    inherit (bashBuilders) mkBashScript;
    inherit pkgs;
  };
in
{
  inherit sync-projects;
  inherit (sync-projects) packages tldr;
  checks = {
    test-sync-projects = sync-projects.check;
  };
}
