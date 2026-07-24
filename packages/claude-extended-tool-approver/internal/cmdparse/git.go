package cmdparse

import "strings"

// GitInvocation parses a git command's pre-subcommand options (the slice AFTER the
// `git` executable), returning the ordered `-C <path>` chdir values, the subcommand
// ("" if none), and the args after it. It consumes the option-arg for
// -C/-c/--git-dir/--work-tree/--namespace exactly as git does.
func GitInvocation(args []string) (chdirs []string, subcmd string, rest []string) {
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "-C":
			if i+1 < len(args) {
				chdirs = append(chdirs, args[i+1])
			}
			i += 2
			continue
		case "-c", "--git-dir", "--work-tree", "--namespace":
			i += 2
			continue
		default:
			if strings.HasPrefix(a, "-") {
				i++
				continue
			}
			return chdirs, a, args[i+1:]
		}
	}
	return chdirs, "", nil
}
