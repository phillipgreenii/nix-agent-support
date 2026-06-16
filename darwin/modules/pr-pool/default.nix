{
  config,
  lib,
  ...
}:
let
  obs = config.phillipgreenii.observability;
in
{
  # phillipgreenii.observability.logSources is declared at darwin/system scope in
  # phillipgreenii-nix-support-apps (darwin/modules/observability/registration.nix),
  # so this lives in darwin, not in the home-manager module — setting it from HM
  # targets an undeclared option and fails eval (same reasoning as pa-monitor's
  # dashboardProviders registration).
  #
  # pr-pool writes its JSONL event log to the standard path
  # ${XDG_STATE_HOME}/pr-pool/events.jsonl, which the default `path` glob
  # (${env:XDG_STATE_HOME}/pr-pool/*.jsonl) already matches — so no overrides are
  # needed. Guarded on obs.enable so it is a no-op on machines without the stack.
  config = lib.mkIf (obs.enable or false) {
    phillipgreenii.observability.logSources.pr-pool = { };
  };
}
