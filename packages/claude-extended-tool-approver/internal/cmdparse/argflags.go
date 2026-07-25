package cmdparse

import "strings"

// This file holds shared argument-flag parsing helpers for grep/rg and jq —
// relocated here (pg2-ia640.2) from the safecmds rule so the sibling secrets
// rule can reuse them without importing a rules package. Both callers need to
// separate a command's real FILE-path arguments from its non-path arguments
// (search patterns, replacement/glob/context values, jq variables and filters)
// before running their path/secret checks.

// grepFlagsWithValue lists grep flags that consume the next argument as a value
// (so that value is NOT a searched file path). These forms are valid for rg too.
var grepFlagsWithValue = map[string]bool{
	"-e": true, "--regexp": true,
	"-f": true, "--file": true,
	"-m": true, "--max-count": true,
	"-A": true, "--after-context": true,
	"-B": true, "--before-context": true,
	"-C": true, "--context": true,
	"--include": true, "--exclude": true, "--exclude-dir": true,
	"--label": true, "--color": true, "--colours": true,
}

// rgFlagsWithValue lists ripgrep-specific flags that consume the next argument.
// Their SHORT forms -r/-E/-T are value-consuming in rg but BOOLEAN in grep
// (-r=recursive, -E=extended-regexp, -T=initial-tab), so they are honored only
// for rg — folding them into grepFlagsWithValue would make grep mis-skip a real
// file path (e.g. `grep -r pat /secrets/x` would drop /secrets/x). (pg2-ia640.2)
var rgFlagsWithValue = map[string]bool{
	"-g": true, "--glob": true, "--iglob": true,
	"-t": true, "--type": true,
	"-T": true, "--type-not": true,
	"--type-add": true,
	"-r":         true, "--replace": true,
	"-M": true, "--max-columns": true,
	"--sort": true, "--sortr": true,
	"-E": true, "--encoding": true,
	"--engine":      true,
	"--pre":         true,
	"--ignore-file": true,
	"-d":            true, "--max-depth": true,
}

// SkipGrepPattern returns args with the positional search PATTERN removed (grep
// and rg take the pattern as the first non-flag argument, which is not a file)
// and the value of every value-consuming flag removed, leaving only file-path
// arguments for downstream path/secret checks.
//
// When -e/--regexp or -f/--file is present there is NO positional pattern (the
// pattern(s) come from those flags), so every positional is a file; that branch
// still strips each value-flag's value so the pattern / pattern-file value is
// itself not mistaken for a searched path (fixes the prior unstripped-branch bug
// where `grep -e .env file.log` leaked `.env`). (pg2-ia640.2)
//
// cmd selects the flag vocabulary: "rg" additionally honors rgFlagsWithValue
// (see its doc for why the conflicting short flags are rg-only).
func SkipGrepPattern(cmd string, args []string) []string {
	isValueFlag := func(a string) bool {
		if grepFlagsWithValue[a] {
			return true
		}
		return cmd == "rg" && rgFlagsWithValue[a]
	}
	// -e/--regexp and -f/--file supply the pattern(s), so there is no positional
	// pattern to skip; every positional is a file.
	patternSkipped := false
	for _, a := range args {
		if a == "-e" || a == "--regexp" || a == "-f" || a == "--file" {
			patternSkipped = true
			break
		}
	}
	var result []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			// Everything after -- is files.
			result = append(result, args[i:]...)
			break
		}
		if isValueFlag(a) && i+1 < len(args) {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			i++
			continue
		}
		if !patternSkipped {
			patternSkipped = true
			i++
			continue // skip the positional pattern
		}
		result = append(result, a)
		i++
	}
	return result
}

// jqValueFlags lists jq flags that consume two value arguments (name value).
// These arguments may look like paths (e.g. --arg dir "/app/src") but are jq
// variables, not file references.
var jqValueFlags = map[string]bool{
	"--arg": true, "--argjson": true,
	"--slurpfile": true, "--rawfile": true, "--jsonargs": true,
}

// jqOneArgFlags lists jq flags that consume one value argument.
var jqOneArgFlags = map[string]bool{
	"--indent": true, "--tab": true, "--from-file": true, "--jsonargs": true,
	"-f": true, "--join-output": true,
}

// SkipJqValueFlags returns the args with jq value-flag arguments removed, so path
// checking only sees actual file arguments.
func SkipJqValueFlags(args []string) []string {
	var result []string
	i := 0
	for i < len(args) {
		a := args[i]
		if jqValueFlags[a] && i+2 < len(args) {
			i += 3 // skip flag + name + value
			continue
		}
		if jqOneArgFlags[a] && i+1 < len(args) {
			i += 2 // skip flag + value
			continue
		}
		result = append(result, a)
		i++
	}
	return result
}
