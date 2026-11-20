package main

import (
	"fmt"
	"strings"
)

var subcommands = []string{
	"lease", "renew", "release", "expire", "info", "list", "preserved", "reap",
	"config", "completion", "serve", "version", "help",
}

var commonCompletionFlags = []string{"--repo", "--format", "--json"}

// completionFlags are the flags each subcommand takes, beyond the common ones.
var completionFlags = map[string][]string{
	"lease":      {"--name", "--or-renew", "--owner", "--metadata", "--ttl", "-b", "-B", "--detach", "--wait", "--print"},
	"renew":      {"--name", "--ttl", "--metadata", "--print"},
	"release":    {"--name"},
	"expire":     {"--name"},
	"info":       {"--path", "--id", "--lease-id", "--name", "--print"},
	"list":       {"--include-unmanaged", "--exceptions"},
	"preserved":  {"--older-than", "--dry-run"},
	"reap":       {"--all-repos", "--dry-run", "--deadline"},
	"config":     {"--config"},
	"completion": {},
}

func (a *app) completion(args []string) int {
	if len(args) != 1 {
		return a.usageErr(fmt.Errorf("usage: pg-gwm completion bash|zsh"))
	}
	switch args[0] {
	case "bash":
		fmt.Fprint(a.stdout, bashCompletion())
	case "zsh":
		fmt.Fprint(a.stdout, zshCompletion())
	default:
		return a.usageErr(fmt.Errorf("completion: unknown shell %q (want bash or zsh)", args[0]))
	}
	return 0
}

func bashCompletion() string {
	var b strings.Builder
	b.WriteString("# bash completion for pg-gwm\n_pg_gwm() {\n  local cur=\"${COMP_WORDS[COMP_CWORD]}\"\n  local sub=\"${COMP_WORDS[1]}\"\n")
	fmt.Fprintf(&b, "  if [ \"$COMP_CWORD\" -eq 1 ]; then\n    COMPREPLY=($(compgen -W %q -- \"$cur\"))\n    return\n  fi\n", strings.Join(subcommands, " "))
	b.WriteString("  case \"$sub\" in\n")
	for _, s := range subcommands {
		fl, ok := completionFlags[s]
		if !ok {
			continue
		}
		all := append(append([]string{}, commonCompletionFlags...), fl...)
		fmt.Fprintf(&b, "    %s) COMPREPLY=($(compgen -W %q -- \"$cur\")) ;;\n", s, strings.Join(all, " "))
	}
	b.WriteString("  esac\n}\ncomplete -F _pg_gwm pg-gwm\n")
	return b.String()
}

func zshCompletion() string {
	var b strings.Builder
	b.WriteString("#compdef pg-gwm\n# zsh completion for pg-gwm\n_pg_gwm() {\n  local -a subs\n")
	fmt.Fprintf(&b, "  subs=(%s)\n", strings.Join(subcommands, " "))
	b.WriteString("  if (( CURRENT == 2 )); then\n    _describe 'command' subs\n    return\n  fi\n  case \"$words[2]\" in\n")
	for _, s := range subcommands {
		fl, ok := completionFlags[s]
		if !ok {
			continue
		}
		all := append(append([]string{}, commonCompletionFlags...), fl...)
		fmt.Fprintf(&b, "    %s) compadd -- %s ;;\n", s, strings.Join(all, " "))
	}
	b.WriteString("  esac\n}\ncompdef _pg_gwm pg-gwm\n")
	return b.String()
}
