# Shared agent script builder
# Extracts common patterns while keeping provider-specific logic separate
{
  # Create an agent script with shared argument parsing, validation, and error handling
  # Usage: mkAgentScript {
  #   name = "my-agent";  # unique identifier for this agent provider
  #   modelMapper = ''
  #     case "$model" in
  #       small)   agent_model="haiku" ;;
  #       medium)  agent_model="sonnet" ;;
  #       large)   agent_model="opus" ;;
  #       *)       agent_model="sonnet" ;;
  #     esac
  #   '';
  #   commandBuilder = ''
  #     agent_cmd="my-agent --model $agent_model"
  #     if [[ "$plan" == "true" ]]; then
  #       agent_cmd="$agent_cmd --plan"
  #     fi
  #   '';
  #   thinkingHandler = ''  # optional
  #     if [[ "$thinking" == "true" ]]; then
  #       agent_model="$agent_model-thinking"
  #     fi
  #   '';
  #   extraAuthPatterns = [ "not.*logged.*in" ];  # optional
  # }
  mkAgentScript =
    {
      name,
      modelMapper,
      commandBuilder,
      thinkingHandler ? "",
      extraAuthPatterns ? [ ],
      timeoutBin ? "timeout",
      timeoutSeconds ? 45,
    }:
    ''
      # ${name} Agent Script for zr-agent
      # Accepts positional arguments: <model> <plan> <thinking> <prompt>
      # Exit codes: 0=success, 1=error, 11=auth error, 12=license/usage error

      set -euo pipefail

      # Parse positional arguments
      model="''${1:-medium}"
      plan="''${2:-false}"
      thinking="''${3:-false}"
      prompt="''${4:-}"

      # Map generic model names to provider-specific models
      ${modelMapper}

      # Validate we have a prompt
      if [[ -z "$prompt" ]]; then
        echo "Error: No prompt provided" >&2
        exit 1
      fi

      # Read from stdin if prompt is "-"
      if [[ "$prompt" == "-" ]]; then
        prompt=$(cat)
        if [[ -z "$prompt" ]]; then
          echo "Error: No input from stdin" >&2
          exit 1
        fi
      fi

      # Apply thinking mode handler if provided
      ${thinkingHandler}

      # Build command
      ${commandBuilder}

      # Execute command with timeout
      if output=$("${timeoutBin}" "${toString timeoutSeconds}" bash -c 'echo "$1" | eval "$2"' -- "$prompt" "$agent_cmd" 2>&1); then
        exit_code=$?
      else
        exit_code=$?
      fi

      # Check for timeout (exit code 124)
      if [[ $exit_code -eq 124 ]]; then
        echo "Error: ${name} timed out after ${toString timeoutSeconds}s" >&2
        exit 1
      fi

      # Check for specific error patterns and map to appropriate exit codes
      if [[ $exit_code -ne 0 ]]; then
        # Check for authentication errors
        auth_patterns="authentication\|unauthorized\|invalid.*key\|api.*key"
        ${
          if extraAuthPatterns != [ ] then
            ''auth_patterns="$auth_patterns\|${builtins.concatStringsSep "\\|" extraAuthPatterns}"''
          else
            ""
        }

        if echo "$output" | grep -qi "$auth_patterns"; then
          echo "Error: ${name} authentication failed" >&2
          exit 11
        fi

        # Check for license/usage errors
        if echo "$output" | grep -qi "quota\|rate.*limit\|usage.*limit\|no.*tokens\|insufficient.*credits\|subscription"; then
          echo "Error: ${name} usage limit reached" >&2
          exit 12
        fi

        # General error
        echo "Error: ${name} failed: $output" >&2
        exit 1
      fi

      # Check if output is empty
      if [[ -z "$output" ]]; then
        echo "Error: ${name} returned empty output" >&2
        exit 1
      fi

      # Success - output the result
      echo "$output"
      exit 0
    '';
}
