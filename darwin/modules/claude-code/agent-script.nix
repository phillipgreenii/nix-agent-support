{ pkgs }:
let
  agentScriptLib = import ../../lib/agent-script.nix;
in
agentScriptLib.mkAgentScript {
  name = "Claude";
  timeoutBin = "${pkgs.coreutils}/bin/timeout";

  modelMapper = ''
    case "$model" in
      small)   claude_model="haiku" ;;
      medium)  claude_model="sonnet" ;;
      large)   claude_model="opus" ;;
      *)       claude_model="sonnet" ;;  # fallback to medium
    esac
    agent_model="$claude_model"
  '';

  # Note: The thinking mode is handled automatically by Claude
  thinkingHandler = "";

  commandBuilder = ''
    # Build Claude command
    agent_cmd="claude -p"

    # Add plan mode if requested
    if [[ "$plan" == "true" ]]; then
      agent_cmd="$agent_cmd --permission-mode plan"
    fi

    # Add output format and skip session persistence
    agent_cmd="$agent_cmd --output-format text --no-session-persistence"

    # Add model selection (using mapped Claude model name)
    agent_cmd="$agent_cmd --model $agent_model"
  '';
}
