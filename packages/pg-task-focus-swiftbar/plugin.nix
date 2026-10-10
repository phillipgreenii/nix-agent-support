# The generated SwiftBar plugin wrapper for pg-task-focus.
#
# This is a FUNCTION, not a fixed overlay attr, because the wrapper bakes in
# per-configuration values (the pg-task-focus binary the daemon matches, the
# daemon address, the URL "Open pg-task-focus" opens, the due-soon window and
# the boost sizes). The Home-Manager module calls `mkPluginWrapper` with its own
# `cfg`; the flake check `test-pg-task-focus-swiftbar-plugin` builds the SAME
# function with defaults and asserts the wrapper's shape via `mkWrapperCheck`.
#
# STREAMING: the wrapper carries `<swiftbar.type>streamable</swiftbar.type>`, which
# SwiftBar 2.0.1's shipped README documents as the marker for a long-lived
# streamable plugin. The frame separator itself is the renderer's one seam
# (STREAM_SEPARATOR in pg-task-focus-swiftbar.sh), with the verified and the
# unverified parts of the syntax stated there.
{ lib, pkgs }:
let
  # Every plugin metadata tag, one per line, in output order. The wrapper check
  # derives its expectations from this same list so tag drift cannot pass.
  tags = {
    "xbar.title" = "pg-task-focus";
    "xbar.version" = "v1.0";
    "xbar.author" = "phillipgreenii";
    "xbar.desc" =
      "pg-task-focus in the menu bar: the running cycle and its timer, overtime, paused cycles with a resume or switch action, the next due task and a READ-ONLY marker when the daemon cannot write. Streams from `pg-task-focus status --watch`";
    "xbar.dependencies" = "pg-task-focus";
    "swiftbar.type" = "streamable";
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
    "swiftbar.type"
    "swiftbar.hideRunInTerminal"
    "swiftbar.hideDisablePlugin"
    "swiftbar.hideSwiftBar"
  ];
  hideTags = lib.filter (t: lib.hasPrefix "swiftbar.hide" t) tagOrder;

  # A streamable plugin has no refresh interval: the process runs until SwiftBar
  # stops it, so the filename carries none (`{name}.{time}.{ext}`, time optional).
  pluginFileName = "pg-task-focus.sh";

  mkPluginWrapper =
    {
      script,
      pgTaskFocus,
      addr ? "127.0.0.1:49210",
      webUrl ? "http://${addr}",
      dueSoonMinutes ? 30,
      boostMinutes ? [
        5
        10
        25
      ],
    }:
    let
      text = ''
        #!/usr/bin/env bash
        ${lib.concatMapStringsSep "\n" (t: "# <${t}>${tags.${t}}</${t}>") tagOrder}
        # SwiftBar runs plugins with a minimal environment; put the per-user, the
        # nix-profile and the system bins ahead of whatever it provides.
        export PATH="/etc/profiles/per-user/$(id -un)/bin:$HOME/.nix-profile/bin:/run/current-system/sw/bin:$PATH"
        # The client must match the running daemon, so the Nix build bakes it in.
        export PG_TASK_FOCUS_BIN=${lib.escapeShellArg "${pgTaskFocus}/bin/pg-task-focus"}
        export PG_TASK_FOCUS_ADDR=${lib.escapeShellArg addr}
        export PG_TASK_FOCUS_SWIFTBAR_WEB_URL=${lib.escapeShellArg webUrl}
        export PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN=${lib.escapeShellArg (toString dueSoonMinutes)}
        export PG_TASK_FOCUS_SWIFTBAR_BOOST_MINUTES=${
          lib.escapeShellArg (lib.concatMapStringsSep " " toString boostMinutes)
        }
        exec ${lib.escapeShellArg "${script}/bin/pg-task-focus-swiftbar"} "$@"
      '';
    in
    pkgs.writeTextFile {
      name = "pg-task-focus-swiftbar-plugin";
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
    { script, pgTaskFocus }:
    let
      wrapper = mkPluginWrapper { inherit script pgTaskFocus; };
    in
    pkgs.runCommand "test-pg-task-focus-swiftbar-plugin" { nativeBuildInputs = [ pkgs.gnugrep ]; } ''
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

      # The streaming marker: without it SwiftBar runs the plugin once and
      # treats a never-ending process as a hang.
      [ "$(sed -n 's|^# <swiftbar.type>\(.*\)</swiftbar.type>$|\1|p' "$w")" = streamable ] || fail "swiftbar.type must be streamable"

      n=$(grep -c '^# <\(xbar\|swiftbar\)\.' "$w")
      [ "$n" -eq ${toString (builtins.length tagOrder)} ] || fail "expected ${toString (builtins.length tagOrder)} tags, found $n"

      exec_line=$(grep -n '^exec ' "$w" | head -n1 | cut -d: -f1)
      [ -n "$exec_line" ] || fail "no exec line"
      for v in PG_TASK_FOCUS_BIN PG_TASK_FOCUS_ADDR PG_TASK_FOCUS_SWIFTBAR_WEB_URL PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN PG_TASK_FOCUS_SWIFTBAR_BOOST_MINUTES; do
        line=$(grep -n "^export $v=" "$w" | head -n1 | cut -d: -f1)
        [ -n "$line" ] || fail "no $v export"
        [ "$line" -lt "$exec_line" ] || fail "$v export must precede exec"
      done
      grep -qxF 'export PG_TASK_FOCUS_BIN=${pgTaskFocus}/bin/pg-task-focus' "$w" || fail "PG_TASK_FOCUS_BIN not baked to the pg-task-focus package"
      grep -qx 'export PG_TASK_FOCUS_ADDR=127.0.0.1:49210' "$w" || fail "default address not baked"
      grep -qx 'export PG_TASK_FOCUS_SWIFTBAR_WEB_URL=http://127.0.0.1:49210' "$w" || fail "default web URL not baked"
      grep -qx 'export PG_TASK_FOCUS_SWIFTBAR_DUE_SOON_MIN=30' "$w" || fail "default due-soon window not baked"
      grep -qx "export PG_TASK_FOCUS_SWIFTBAR_BOOST_MINUTES='5 10 25'" "$w" || fail "default boost sizes not baked"

      target=$(sed -n 's|^exec \([^ ]*\) .*|\1|p' "$w")
      [ -x "$target" ] || fail "exec target $target is not an executable file"
      [ "$target" = ${script}/bin/pg-task-focus-swiftbar ] || fail "exec target is not the renderer package"

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
