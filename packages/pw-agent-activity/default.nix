{ pkgs, ... }:
pkgs.writeShellScriptBin "pw-agent-activity" ''
  exec agent-activity-api wait "$@"
''
