{ pkgs, ... }:
pkgs.writeShellScriptBin "pw-reset-agents" ''
  exec agent-activity-api clean "$@"
''
