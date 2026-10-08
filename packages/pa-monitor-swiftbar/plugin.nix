# The generated SwiftBar plugin wrapper for pa-monitor.
#
# This is a FUNCTION, not a fixed overlay attr, because the wrapper bakes in
# per-configuration values (the pa-monitor binary the daemon matches and the
# stale threshold). The Home-Manager module calls `mkPluginWrapper` with its own
# `cfg.package` and `cfg.swiftbar.staleAfterS`; the flake check
# `test-pa-monitor-swiftbar-plugin` builds the SAME function with defaults and
# asserts the wrapper's shape via `mkWrapperCheck`.
{ lib, pkgs }:
let
  # Every plugin metadata tag, one per line, in output order. The wrapper check
  # derives its expectations from this same list so tag drift cannot pass.
  tags = {
    "xbar.title" = "pa-monitor";
    "xbar.version" = "v1.0";
    "xbar.author" = "phillipgreenii";
    "xbar.desc" =
      "5-hour Claude usage window, usage-limit countdown, and caffeinate / auto-resume toggles from pa-monitor";
    "xbar.dependencies" = "pa-monitor";
    "swiftbar.hideRunInTerminal" = "true";
    "swiftbar.hideDisablePlugin" = "true";
    "swiftbar.hideSwiftBar" = "true";
  };
  tagOrder = [
    "xbar.title"
    "xbar.version"
    "xbar.author"
    "xbar.desc"
    "xbar.dependencies"
    "swiftbar.hideRunInTerminal"
    "swiftbar.hideDisablePlugin"
    "swiftbar.hideSwiftBar"
  ];
  hideTags = lib.filter (t: lib.hasPrefix "swiftbar.hide" t) tagOrder;

  # The refresh interval is part of the plugin FILENAME (SwiftBar convention),
  # owned by the caller that installs the file.
  pluginFileName = "pa-monitor.30s.sh";

  mkPluginWrapper =
    {
      script,
      paMonitor,
      staleAfterS ? 600,
    }:
    let
      text = ''
        #!/usr/bin/env bash
        ${lib.concatMapStringsSep "\n" (t: "# <${t}>${tags.${t}}</${t}>") tagOrder}
        # SwiftBar runs plugins with a minimal environment; put the per-user, the
        # nix-profile and the system bins ahead of whatever it provides.
        export PATH="/etc/profiles/per-user/$(id -un)/bin:$HOME/.nix-profile/bin:/run/current-system/sw/bin:$PATH"
        # The pa-monitor client must match the running daemon, so the Nix build bakes it in.
        export PA_MONITOR_BIN=${lib.escapeShellArg "${paMonitor}/bin/pa-monitor"}
        export PA_SWIFTBAR_STALE_AFTER_S=${lib.escapeShellArg (toString staleAfterS)}
        exec ${lib.escapeShellArg "${script}/bin/pa-monitor-swiftbar"} "$@"
      '';
    in
    pkgs.writeTextFile {
      name = "pa-monitor-swiftbar-plugin";
      executable = true;
      inherit text;
      # `text` is exposed so a pure-eval test can inspect the wrapper source
      # without building it (no import-from-derivation).
      passthru = { inherit text; };
    };

  # Flake check: build the wrapper with defaults and assert its shape. The HM
  # render test cannot see file modes, so the executable bit of the store file
  # itself is asserted here.
  mkWrapperCheck =
    { script, paMonitor }:
    let
      wrapper = mkPluginWrapper { inherit script paMonitor; };
    in
    pkgs.runCommand "test-pa-monitor-swiftbar-plugin" { nativeBuildInputs = [ pkgs.gnugrep ]; } ''
      w=${wrapper}
      fail() { echo "FAIL: $*" >&2; exit 1; }

      [ -f "$w" ] || fail "wrapper is not a regular file"
      [ -x "$w" ] || fail "wrapper store file lacks the executable bit"
      [ "$(head -n1 "$w")" = '#!/usr/bin/env bash' ] || fail "bad shebang"

      ${lib.concatMapStringsSep "\n" (t: ''
        v=$(sed -n 's|^# <${t}>\(.*\)</${t}>$|\1|p' "$w")
        [ -n "$v" ] || fail "tag ${t} missing or empty"
      '') tagOrder}
      ${lib.concatMapStringsSep "\n" (t: ''
        [ "$(sed -n 's|^# <${t}>\(.*\)</${t}>$|\1|p' "$w")" = true ] || fail "tag ${t} must be true"
      '') hideTags}

      n=$(grep -c '^# <\(xbar\|swiftbar\)\.' "$w")
      [ "$n" -eq ${toString (builtins.length tagOrder)} ] || fail "expected ${toString (builtins.length tagOrder)} tags, found $n"

      export_line=$(grep -n '^export PA_MONITOR_BIN=' "$w" | head -n1 | cut -d: -f1)
      exec_line=$(grep -n '^exec ' "$w" | head -n1 | cut -d: -f1)
      [ -n "$export_line" ] || fail "no PA_MONITOR_BIN export"
      [ -n "$exec_line" ] || fail "no exec line"
      [ "$export_line" -lt "$exec_line" ] || fail "PA_MONITOR_BIN export must precede exec"
      grep -qF 'export PA_MONITOR_BIN=${paMonitor}/bin/pa-monitor' "$w" || fail "PA_MONITOR_BIN not baked to the pa-monitor package"
      grep -q '^export PA_SWIFTBAR_STALE_AFTER_S=600$' "$w" || fail "default stale threshold not baked"

      target=$(sed -n 's|^exec \([^ ]*\) .*|\1|p' "$w")
      [ -x "$target" ] || fail "exec target $target is not an executable file"
      [ "$target" = ${script}/bin/pa-monitor-swiftbar ] || fail "exec target is not the renderer package"

      touch $out
    '';
in
{
  inherit
    mkPluginWrapper
    mkWrapperCheck
    pluginFileName
    tags
    ;
}
