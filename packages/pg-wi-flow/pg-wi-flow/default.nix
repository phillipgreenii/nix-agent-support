{
  mkBashScript,
  pkgs,
  pgWiFlowLib,
  testSupport ? null,
}:
mkBashScript {
  name = "pg-wi-flow";
  src = ./.;
  description = "pg-wi-flow: bead-workflow CLI framework (query, list, next, claim, release, context, explain, history, duplicates, docs; more subcommands land in later tc-9ddu3.1 packets)";
  libraries = [
    pgWiFlowLib.actor
    pgWiFlowLib.config
    pgWiFlowLib.tracker
    pgWiFlowLib.context
  ];
  # jq: every verb reads/builds JSON. git: pgwf_config_repo_path (via
  # lib/config.bash) resolves the repo config layer's location. coreutils:
  # escalate's fingerprint hashing (`sha256sum`, tc-9ddu3.1.4) -- same
  # precedent as packages/claude-activity's get_session_id, which also adds
  # coreutils explicitly for this reason. `bd` itself is deliberately NOT
  # listed here -- it is not packaged in this flake (an ambient tool the
  # agent's own environment already provides, same posture as this
  # workspace's other bd-invoking tooling).
  runtimeDeps = [
    pkgs.jq
    pkgs.git
    pkgs.coreutils
  ];
  testDeps = [
    pkgs.jq
    pkgs.git
    pkgs.coreutils
  ];
  inherit testSupport;
}
