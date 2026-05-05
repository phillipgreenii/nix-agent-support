# claude-agents-tui.bash
_claude_agents_tui() {
  local cur
  cur="${COMP_WORDS[COMP_CWORD]}"
  COMPREPLY=($(compgen -W "--wait-until-idle --maximum-wait --time-between-checks --consecutive-idle-checks --caffeinate --version --help" -- "$cur"))
}
complete -F _claude_agents_tui claude-agents-tui
