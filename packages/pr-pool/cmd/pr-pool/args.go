package main

import "flag"

// parseInterspersed parses a FlagSet allowing flags to appear before, after, or
// between positional arguments. Go's stdlib flag stops at the first positional,
// silently dropping any flags after it. This walks the args, collecting
// positionals and re-parsing the remainder. Returns the positionals.
func parseInterspersed(fs *flag.FlagSet, args []string) []string {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return positionals
		}
		if fs.NArg() == 0 {
			return positionals
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
