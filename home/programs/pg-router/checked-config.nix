# (import ./checked-config.nix { inherit pkgs package; }).mkChecked name text
#
# Renders `text` as the store file `name` (the plain writeText path) AND fails the build unless the
# packaged pg-router binary's own config loader accepts it. A system generation
# can otherwise pair a new binary with a config in an older schema; the daemon
# then exits at startup and launchd restarts it in a loop for as long as that
# generation stays applied.
#
# The check is `pg-router config check --offline`: the daemon's exact startup
# load (strict key decode, role and query validation, wiring checks), minus the
# probe that every source and handler command exists, because a build sandbox
# has none of the machine's commands. The loader's error lines become the
# build log.
# `checkScript name rendered` is the shell the build runs, exposed so a flake
# check can run the same lines over a negative fixture (a build failure cannot
# be asserted from inside the sandbox).
{ pkgs, package }:
rec {
  checkScript = name: rendered: ''
    export HOME="$TMPDIR/home"
    mkdir -p "$HOME"
    export XDG_STATE_HOME="$HOME/state" XDG_CONFIG_HOME="$HOME/config"
    export PG_ROUTER_REPO_ROOT="$TMPDIR" PG_ROUTER_CONFIG=${rendered}
    export PG_ROUTER_GLOBAL_CONFIG="$HOME/no-global.toml" PG_ROUTER_LOG_DIR="$HOME/log"
    if ! ${package}/bin/pg-router config check --offline >"$TMPDIR/check.out" 2>"$TMPDIR/check.err"; then
      echo "pg-router ${name}: the packaged pg-router rejects this rendered config:" >&2
      cat "$TMPDIR/check.err" >&2
      exit 1
    fi
  '';

  # The result is the plain writeText store path (content-addressed by name
  # and text, so pg-desk-serve's own spelling of the same writeText still names
  # the very file the router runs on), carrying the check derivation as string
  # context: whatever interpolates it must build the check first, and a rejected
  # config fails that build.
  mkChecked =
    name: text:
    let
      rendered = pkgs.writeText name text;
      check = pkgs.runCommand "${name}-check" { } ''
        ${checkScript name rendered}
        touch "$out"
      '';
    in
    pkgs.lib.addContextFrom check "${rendered}";
}
