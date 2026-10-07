// Package shim classifies gh and bd invocations as reads or writes. The shim
// scripts in the scratch bin directory call it, log every argv with its
// verdict, and either exec the real tool (a known read) or refuse.
package shim

import (
	"regexp"
	"strings"
)

// Verdict is the classification of one invocation.
type Verdict string

const (
	// Allow: a known read.
	Allow Verdict = "allow"
	// RejectWrite: a verb known to write.
	RejectWrite Verdict = "reject-write"
	// RejectUnknown: not known to be a read, so refused.
	RejectUnknown Verdict = "reject-unknown"
)

var mutationRE = regexp.MustCompile(`(?i)\bmutation\b`)

var writeMethods = map[string]bool{"POST": true, "PATCH": true, "PUT": true, "DELETE": true}

// ghWriteVerbs are `gh <group> <verb>` pairs that write (named for the
// verdict reason; anything not on the read list is refused regardless).
var ghWriteGroups = map[string]map[string]bool{
	"pr":    set("create", "merge", "review", "comment", "edit", "close", "reopen", "ready", "lock", "unlock", "update-branch", "checkout"),
	"issue": set("create", "comment", "edit", "close", "reopen", "delete", "lock", "unlock", "transfer", "pin", "unpin", "develop"),
	"repo":  set("create", "delete", "edit", "fork", "rename", "archive", "unarchive", "sync", "clone", "set-default"),
}

var ghWriteTop = set("release", "gist", "label", "workflow", "secret", "variable", "ruleset", "codespace", "ssh-key", "gpg-key", "cache", "extension", "alias", "project")

var (
	ghReadPR   = set("view", "list", "status", "checks", "diff")
	ghReadRepo = set("view", "list")
	ghReadRun  = set("list", "view")
)

func set(v ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	return m
}

// ClassifyGH classifies one `gh` argv (without the program name).
func ClassifyGH(args []string) (Verdict, string) {
	pos := positional(args, ghValueFlags)
	if len(pos) == 0 {
		for _, a := range args {
			if a == "--version" || a == "-h" || a == "--help" {
				return Allow, "version/help"
			}
		}
		return RejectUnknown, "no verb"
	}
	top := pos[0]
	switch top {
	case "version":
		return Allow, "version"
	case "api":
		return classifyAPI(args)
	case "search":
		return Allow, "search"
	case "auth":
		if len(pos) > 1 && (pos[1] == "token" || pos[1] == "status") {
			return Allow, "auth " + pos[1]
		}
		return RejectWrite, "auth " + strings.Join(pos[1:], " ")
	case "config":
		if len(pos) > 1 && (pos[1] == "get" || pos[1] == "list") {
			return Allow, "config " + pos[1]
		}
		return RejectWrite, "config write"
	case "pr", "issue", "repo", "run":
		if len(pos) < 2 {
			return RejectUnknown, top + " without a verb"
		}
		verb := pos[1]
		if w, ok := ghWriteGroups[top]; ok && w[verb] {
			return RejectWrite, top + " " + verb
		}
		read := map[string]map[string]bool{"pr": ghReadPR, "issue": ghReadPR, "repo": ghReadRepo, "run": ghReadRun}[top]
		if read[verb] {
			return Allow, top + " " + verb
		}
		return RejectUnknown, top + " " + verb
	}
	if ghWriteTop[top] {
		return RejectWrite, top
	}
	return RejectUnknown, top
}

// ghValueFlags are gh flags whose value is the next argument, so it is not a
// verb.
var ghValueFlags = set("-R", "--repo", "-X", "--method", "-f", "-F", "--field", "--raw-field", "-H", "--header", "--jq", "-q", "-t", "--template", "--json", "--hostname", "--input", "--cache", "-L", "--limit", "-s", "--state", "-A", "--author", "-S", "--search", "-B", "--base", "--head", "-l", "--label", "-b", "--branch", "-w", "--workflow", "-e", "--event", "-u", "--user")

// positional returns the non-flag arguments, skipping the value of flags that
// take a separate one. `--flag=value` forms are a single token.
func positional(args []string, valueFlags map[string]bool) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") {
			if valueFlags[a] && i+1 < len(args) {
				i++
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

func classifyAPI(args []string) (Verdict, string) {
	method := ""
	hasBody := false
	graphql := false
	var queries []string
	endpointSeen := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "api" && !endpointSeen:
			endpointSeen = true
			continue
		case a == "-X" || a == "--method":
			method = strings.ToUpper(next())
		case strings.HasPrefix(a, "--method="):
			method = strings.ToUpper(strings.TrimPrefix(a, "--method="))
		case a == "-f" || a == "-F" || a == "--field" || a == "--raw-field":
			hasBody = true
			queries = append(queries, next())
		case strings.HasPrefix(a, "-f") && len(a) > 2 && !strings.HasPrefix(a, "--"):
			hasBody = true
			queries = append(queries, a[2:])
		case strings.HasPrefix(a, "-F") && len(a) > 2 && !strings.HasPrefix(a, "--"):
			hasBody = true
			queries = append(queries, a[2:])
		case strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field="):
			hasBody = true
			_, v, _ := strings.Cut(a, "=")
			queries = append(queries, v)
		case a == "--input":
			// A body from a file or stdin cannot be inspected here.
			return RejectUnknown, "api --input: body not inspectable"
		case strings.HasPrefix(a, "--input="):
			return RejectUnknown, "api --input: body not inspectable"
		case a == "-H" || a == "--header" || a == "--jq" || a == "-q" || a == "-t" || a == "--template" || a == "--cache" || a == "--hostname":
			next()
		case strings.HasPrefix(a, "-"):
			// boolean flags (--paginate, --slurp, -i, --silent, ...)
		default:
			if a == "graphql" {
				graphql = true
			}
		}
	}
	if writeMethods[method] {
		return RejectWrite, "api " + method
	}
	if graphql {
		for _, q := range queries {
			if mutationRE.MatchString(q) {
				return RejectWrite, "api graphql mutation"
			}
		}
		return Allow, "api graphql query"
	}
	if hasBody && method != "GET" {
		return RejectWrite, "api with a field body implies POST"
	}
	return Allow, "api read"
}

var bdReadVerbs = set("list", "show", "ready", "blocked", "search", "count", "where", "version", "deps", "stats")

var bdValueFlags = set("-C", "--dir", "--db", "--actor", "--format", "--sort", "--limit", "-n", "--status", "-s", "--type", "-t", "--label", "-l", "--assignee", "-a", "--priority", "-p", "--query", "--parent", "--json-envelope")

// ClassifyBD classifies one `bd` argv (without the program name).
func ClassifyBD(args []string) (Verdict, string) {
	// Global flags precede the verb: skip them (and the value of the ones that
	// take one) until the first positional.
	verb := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if a == "--version" {
				return Allow, "version"
			}
			if bdValueFlags[a] && i+1 < len(args) && verb == "" {
				i++
			}
			continue
		}
		if verb == "" {
			verb = a
			continue
		}
		rest = append(rest, a)
	}
	switch {
	case verb == "":
		return RejectUnknown, "no verb"
	case verb == "dep" || verb == "dependency":
		if len(rest) > 0 && (rest[0] == "list" || rest[0] == "tree") {
			return Allow, "dep " + rest[0]
		}
		return RejectWrite, "dep " + strings.Join(rest, " ")
	case bdReadVerbs[verb]:
		return Allow, verb
	case bdWriteVerbs[verb]:
		return RejectWrite, verb
	}
	return RejectUnknown, verb
}

var bdWriteVerbs = set("create", "update", "close", "comment", "comments", "label", "delete", "claim", "init", "import", "export", "sql", "dolt", "reopen", "rename", "rename-prefix", "edit", "link", "unlink", "defer", "undefer", "assign", "set", "migrate", "compact", "restore", "sync", "gc", "config", "remember", "forget", "mol", "gate", "slot", "swarm", "repair")
