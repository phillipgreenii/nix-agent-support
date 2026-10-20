package wrap

import "strings"

// basePath is the fixed PATH of the base environment.
const basePath = "/usr/bin:/bin:/usr/sbin:/sbin"

var baseKeep = []string{"HOME", "USER", "LOGNAME", "TMPDIR", "LANG", "TERM", "COLORTERM"}

// BaseEnv builds the minimal base environment for a payload chain. source and
// the result are in os.Environ() form (NAME=value). It keeps HOME, USER, LOGNAME,
// TMPDIR, LANG, TERM, COLORTERM and each name in extras where they are set in
// source (an empty value counts as set), and adds PATH=/usr/bin:/bin:/usr/sbin:/sbin.
// Names match exactly, never by prefix. A PATH in extras is ignored: the base PATH
// is fixed. When a name repeats in source, the last value wins.
func BaseEnv(source []string, extras []string) []string {
	have := make(map[string]string, len(source))
	for _, kv := range source {
		if i := strings.IndexByte(kv, '='); i > 0 {
			have[kv[:i]] = kv[i+1:]
		}
	}
	var out []string
	seen := map[string]bool{"PATH": true}
	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		if v, ok := have[name]; ok {
			out = append(out, name+"="+v)
		}
	}
	for _, n := range baseKeep {
		add(n)
	}
	for _, n := range extras {
		add(n)
	}
	return append(out, "PATH="+basePath)
}
