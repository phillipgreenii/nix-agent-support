# Completions

Hand-written source files in `completions/`. Required for public scripts. When you change a script's flags, options, or subcommands, you MUST update both completion files.

Copy-ready templates live in `assets/completion.bash` and `assets/_completion.zsh`.

## Bash completion (`completions/<name>.bash`)

```bash
_command_name() {
  local cur
  _init_completion || return

  if [[ $cur == -* ]]; then
    mapfile -t COMPREPLY < <(compgen -W "--help -h --version -v --flag" -- "$cur")
    return
  fi

  _filedir  # complete positional args (files)
}

complete -F _command_name command-name
```

**Use `mapfile -t COMPREPLY < <(compgen ...)`** — NOT `COMPREPLY=($(compgen ...))`. The latter trips shellcheck SC2207 and breaks on filenames with spaces.

## Zsh completion (`completions/_<name>`)

```bash
#compdef command-name

_arguments \
  '(-h --help)'{-h,--help}'[Show help message]' \
  '(-v --version)'{-v,--version}'[Show version information]' \
  '(-f --flag)'{-f,--flag}'[Description]:value:' \
  '*:positional:_files'
```
