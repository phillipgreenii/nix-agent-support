{
  mkBashScript,
  pkgs,
  agent-activity,
}:

mkBashScript {
  name = "pw-reset-agents";
  src = ./.;
  description = "Clear stale AI agent activity markers";
  # Both entries are tools the script actually invokes, declared rather than
  # assumed present on the caller's PATH: `agent-activity-api` (shipped by the
  # agent-activity aggregate) is the delegate, and `cat` backs the --help
  # heredoc. Without them the command works only in a login shell and fails from
  # launchd / `env -i` / a ccpool-spawned session.
  runtimeDeps = [
    agent-activity
    pkgs.coreutils
  ];
  testDeps = [
    agent-activity
    pkgs.coreutils
  ];
}
