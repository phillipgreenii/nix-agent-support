{
  config,
  lib,
  pkgs,
  ...
}:
let
  hmUsers = config.home-manager.users or { };
  sweepEnabledByAnyUser = lib.any (u: u.phillipgreenii.programs.pg-ccaudit.sweep.enable or false) (
    lib.attrValues hmUsers
  );

  # Pick the interval / thinking flag from the first user that set one.
  firstOr =
    default: pick:
    let
      vals = lib.filter (v: v != null) (map pick (lib.attrValues hmUsers));
    in
    if vals == [ ] then default else lib.head vals;

  interval = firstOr 900 (u: u.phillipgreenii.programs.pg-ccaudit.sweep.intervalSeconds or null);
  thinking = firstOr false (u: u.phillipgreenii.programs.pg-ccaudit.sweep.thinking or null);

  pkg = pkgs.pg-ccaudit;

  # XDG_STATE_HOME default for the sweep agent's launchd logs. The plist runs
  # under the primary user; mirror the resolution the sibling agents use.
  primaryUser = config.system.primaryUser or null;
  stateHome = if primaryUser != null then "/Users/${primaryUser}/.local/state" else "/tmp/pg-ccaudit";
in
{
  # The scheduled transcript sweep, as a nix-declared launchd USER agent following
  # the `org.nixos.beads-dolt-server` precedent, registered through the canonical
  # `phillipgreenii.system.launchdServices.userAgents` helper (declared in
  # `phillipgreenii-nix-personal`; never write `launchd.user.agents` directly).
  #
  # Three design decisions are load-bearing here and each one is a rejection of an
  # alternative that looks simpler:
  #
  #   1. A TIMER, NOT A SESSION-END HOOK. A hook only fires when a session ends
  #      cleanly. Abnormally-killed sessions are disproportionately the
  #      interesting ones — a stalled or crashed session IS evidence of the waste
  #      being measured — so hooking session end would systematically drop the
  #      strongest signal in the corpus while appearing to have full coverage.
  #      The pg-ccaudit plugin therefore ships NO hooks at all; a flake check
  #      enforces that.
  #
  #   2. SCHEDULED, NOT SPAWNED BY THE QUERY PATH. This machine carries a standing
  #      invariant against tools that transparently start their own background
  #      server (the beads/dolt no-autostart policy in the sibling beads module is
  #      the same rule). `pg-ccaudit query` and `pg-ccaudit status` therefore
  #      report how far behind the index is and stop; only this agent, or an
  #      explicit `pg-ccaudit ingest`, writes.
  #
  #   3. SINGLE INSTANCE. The writer takes an advisory file lock and a second
  #      concurrent ingest detects it, does nothing and exits ZERO. Two writers
  #      racing on the same transcript's resume offset is the one way this design
  #      could corrupt its own coverage accounting, and an overlapping tick is an
  #      expected event at a ~15 minute cadence, not an error worth logging.
  config = lib.mkIf sweepEnabledByAnyUser {
    phillipgreenii.system.launchdServices.userAgents.pg-ccaudit-ingest = {
      label = "com.phillipg.pg-ccaudit-ingest";
      script = ''
        exec ${pkg}/bin/pg-ccaudit ingest${lib.optionalString thinking " --thinking"}
      '';
      runAtLoad = true;
      # `pg-ccaudit ingest` is a periodic short task (StartInterval), not a
      # long-running daemon — it sweeps and exits. keepAlive defaults to true in
      # the helper, which would make launchd RESTART it on every exit (a ~10s
      # respawn loop). Disable keepAlive so StartInterval is the only re-trigger,
      # and exempt it from the health check, which expects state=running — a state
      # a one-shot never reaches.
      keepAlive = false;
      healthCheck = false;
      serviceConfig = {
        StartInterval = interval; # the periodic re-trigger
        # Surface runtime failures: the agent is keepAlive-off and health-check
        # exempt, so without logs a crashing sweep would be silent — and a silently
        # dead sweep is worse than none, because the index would go stale while
        # every query kept answering from it. The staleness note on the query path
        # is the second line of defence; these logs are the first. launchd creates
        # the parent dir if it is missing.
        StandardErrorPath = "${stateHome}/pg-ccaudit/ingest.err.log";
        StandardOutPath = "${stateHome}/pg-ccaudit/ingest.out.log";
      };
    };
  };
}
