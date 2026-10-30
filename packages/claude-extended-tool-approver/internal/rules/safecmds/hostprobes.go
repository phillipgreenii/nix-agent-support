package safecmds

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/patheval"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/temproot"
)

// Host scalars and scratch writes approved by bead pg2-b1lir W4 and W6 (operator
// ruling pg2-r9kv7, 2026-10-27). Each is a closed-form check: a recognised shape
// is approved, anything else is a refusal that defers to Claude Code's own
// permission flow.

// sysctlKey is a sysctl MIB name: letters, digits, `_` and `.` only. A `=` (the
// write form `sysctl name=value`), a `:` or a path never matches.
var sysctlKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.]*$`)

// sysctlIssue returns why args are not a read-only `sysctl [-n] <name>...`, or "".
// Only `-n` (print the value alone) is accepted as a flag: -w (write), -f (load a
// file of writes), -a (dump everything) and the rest are not part of the ruling.
func sysctlIssue(args []string) string {
	keys := 0
	for _, a := range args {
		switch {
		case a == "-n":
		case strings.HasPrefix(a, "-"):
			return "flag " + a + " is not a plain read (only -n is approved)"
		case sysctlKey.MatchString(a):
			keys++
		default:
			return "operand " + a + " is not a bare sysctl name (a write form sets a value)"
		}
	}
	if keys == 0 {
		return "no sysctl name to read"
	}
	return ""
}

// pmsetTopics are the `pmset -g <topic>` reports. `pmset -g` only GETs; every
// setting verb needs a different flag, which pmsetIssue never accepts.
var pmsetTopics = map[string]bool{
	"live": true, "custom": true, "cap": true, "sched": true, "ups": true, "ps": true,
	"batt": true, "accps": true, "adapter": true, "log": true, "assertions": true,
	"therm": true, "thermlog": true, "rawlog": true, "trace": true, "history": true,
	"historydetailed": true, "stats": true,
}

// pmsetIssue returns why args are not `pmset -g [<topic>]`, or "".
func pmsetIssue(args []string) string {
	if len(args) == 0 || args[0] != "-g" {
		return "only `pmset -g [topic]` (a read) is approved"
	}
	if len(args) == 1 {
		return ""
	}
	if len(args) == 2 && pmsetTopics[args[1]] {
		return ""
	}
	return "pmset -g takes at most one known report name"
}

// scratchDirIssue returns why dir (a directory an approved command would write
// into) is not a writable temp location, or "". The location must be under a temp
// root (internal/temproot: $TMPDIR, /private/var/folders, /private/tmp, /tmp and
// CETA_EXTRA_TEMP_ROOTS) AND writable per the path evaluator, so a temp root the
// evaluator does not grant (an interactive session's $TMPDIR under
// /private/var/folders) still falls back to the prompt.
func scratchDirIssue(dir string, pe *patheval.PathEvaluator, cwd string) string {
	abs := temproot.ResolveOperand(cwd, dir)
	if abs == "" || !temproot.Under(abs) {
		return dir + " is not under a temp root"
	}
	if !pe.Evaluate(abs).CanWrite() {
		return dir + " is not writable"
	}
	return ""
}

// defaultTempDir is where mktemp puts a template-less or -t file: $TMPDIR, else /tmp.
func defaultTempDir() string {
	if t := os.Getenv("TMPDIR"); t != "" {
		return t
	}
	return "/tmp"
}

// evaluateMktemp approves `mktemp` that can only create inside a writable temp
// root: `mktemp [-d] [-q] [-u] [-t prefix] [-p dir] [template...]`. A bare
// invocation and `-t` use $TMPDIR; a relative template is joined onto -p's dir, or
// onto the cwd, as mktemp does.
func evaluateMktemp(args []string, pe *patheval.PathEvaluator, cwd, module string) (hookio.RuleResult, error) {
	var tmplDir string // -p
	var useTmpdir bool // -t
	var templates []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-d" || a == "-q" || a == "-u":
		case a == "-t" && i+1 < len(args):
			useTmpdir = true
			i++ // the prefix operand
		case a == "-p" && i+1 < len(args):
			tmplDir = args[i+1]
			i++
		case strings.HasPrefix(a, "-"):
			return hookio.Refused(module, "safe-commands: mktemp flag "+a+" is not approved (deferred to claude-code)")
		default:
			templates = append(templates, a)
		}
	}
	var dirs []string
	switch {
	case len(templates) == 0:
		dir := tmplDir
		if dir == "" {
			dir = defaultTempDir()
		}
		dirs = append(dirs, dir)
	default:
		for _, t := range templates {
			switch {
			case filepath.IsAbs(t):
				dirs = append(dirs, filepath.Dir(t))
			case tmplDir != "":
				dirs = append(dirs, filepath.Dir(filepath.Join(tmplDir, t)))
			case useTmpdir:
				dirs = append(dirs, filepath.Dir(filepath.Join(defaultTempDir(), t)))
			default:
				dirs = append(dirs, filepath.Dir(filepath.Join(cwd, t)))
			}
		}
	}
	for _, d := range dirs {
		if issue := scratchDirIssue(d, pe, cwd); issue != "" {
			return hookio.Refused(module, "safe-commands: mktemp "+issue+" (deferred to claude-code)")
		}
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: mktemp inside a temp root", Module: module}, nil
}

// tarShortOK are the letters of a bundled tar mode this rule understands, all
// harmless to an extraction: x, f (archive operand), v, z/j/J (decompression), k
// (keep existing), m (do not restore mtime) and p (restore permissions).
const tarShortOK = "xfvzjJkmp"

// evaluateTar approves `tar` EXTRACTION of a readable archive into a writable temp
// root: `tar -xf <archive> [-C <dir>]` and the bundled/long spellings of it. It
// refuses create/list/append modes, an absolute-path or program-running option
// (-P, --absolute-names, -I, --use-compress-program, --to-command, --checkpoint-
// action, ...), a member operand that is absolute or climbs with `..`, and any
// option it does not name. Extraction relies on tar's own defaults that strip a
// leading `/`, refuse `..` members and refuse to write through a symlink that
// leaves the target directory (bsdtar and GNU tar both), which is exactly what -P
// switches off.
func evaluateTar(args []string, pe *patheval.PathEvaluator, cwd, module string) (hookio.RuleResult, error) {
	refuse := func(why string) (hookio.RuleResult, error) {
		return hookio.Refused(module, "safe-commands: tar "+why+" (deferred to claude-code)")
	}
	var archive, dest string
	extract, sawDir, archiveAfterDir := false, false, false
	var members []string
	setArchive := func(v string) bool {
		if archive != "" {
			return false
		}
		archive = v
		archiveAfterDir = sawDir
		return true
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--extract" || a == "--get":
			extract = true
		case a == "--verbose" || a == "--gzip" || a == "--bzip2" || a == "--xz" || a == "--keep-old-files":
		case a == "--file" || a == "--directory":
			if i+1 >= len(args) {
				return refuse("option " + a + " has no operand")
			}
			i++
			if a == "--file" {
				if !setArchive(args[i]) {
					return refuse("names more than one archive")
				}
			} else {
				dest, sawDir = args[i], true
			}
		case strings.HasPrefix(a, "--file="):
			if !setArchive(strings.TrimPrefix(a, "--file=")) {
				return refuse("names more than one archive")
			}
		case strings.HasPrefix(a, "--directory="):
			dest, sawDir = strings.TrimPrefix(a, "--directory="), true
		case a == "-C":
			if i+1 >= len(args) {
				return refuse("option -C has no operand")
			}
			i++
			dest, sawDir = args[i], true
		case strings.HasPrefix(a, "--"):
			return refuse("option " + a + " is not approved")
		case strings.HasPrefix(a, "-") || (i == 0 && a != "" && !strings.ContainsAny(a, "/.")):
			// A bundled mode: `-xzf`, or the traditional dash-less `xzf` as the FIRST
			// argument. Each letter must be one this rule understands; `f` takes the next
			// operand as the archive.
			cluster := strings.TrimPrefix(a, "-")
			if cluster == "" {
				return refuse("has an empty option")
			}
			for _, c := range cluster {
				if !strings.ContainsRune(tarShortOK, c) {
					return refuse("mode letter " + string(c) + " is not approved")
				}
				switch c {
				case 'x':
					extract = true
				case 'f':
					if i+1 >= len(args) {
						return refuse("-f has no archive operand")
					}
					i++
					if !setArchive(args[i]) {
						return refuse("names more than one archive")
					}
				}
			}
		default:
			members = append(members, a)
		}
	}
	if !extract {
		return refuse("is not an extraction (-x)")
	}
	if archive == "" || archive == "-" {
		return refuse("has no archive file operand")
	}
	for _, m := range members {
		if strings.HasPrefix(m, "/") || m == ".." || strings.HasPrefix(m, "../") || strings.Contains(m, "/../") || strings.HasSuffix(m, "/..") {
			return refuse("member operand " + m + " is absolute or climbs with ..")
		}
	}
	if !filepath.IsAbs(archive) && archiveAfterDir {
		return refuse("archive " + archive + " is relative to a -C directory")
	}
	archivePath := archive
	if !filepath.IsAbs(archivePath) {
		archivePath = filepath.Join(cwd, archivePath)
	}
	if !pe.Evaluate(archivePath).CanRead() {
		return refuse("archive references unknown path " + archive)
	}
	target := dest
	if target == "" {
		target = cwd
	}
	if issue := scratchDirIssue(target, pe, cwd); issue != "" {
		return refuse("extraction target " + issue)
	}
	return hookio.RuleResult{Decision: hookio.Approve, Reason: "safe-commands: tar extraction into a temp root", Module: module}, nil
}
