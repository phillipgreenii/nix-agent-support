_bgcheck() {
  local cur
  _init_completion || return

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v --lines -n --dir -d" -- "$cur")
    return
  fi

  # Complete job names from the default state directory.
  local dir="${BG_DIR:-${TMPDIR:-/tmp}/pg-bg-${USER:-$(id -un)}}"
  if [[ -d $dir ]]; then
    local names=()
    local f
    for f in "$dir"/*.pid; do
      [[ -e $f ]] || continue
      names+=("$(basename "${f%.pid}")")
    done
    mapfile -t COMPREPLY < <(compgen -W "${names[*]}" -- "$cur")
  fi
}

complete -F _bgcheck bgcheck
