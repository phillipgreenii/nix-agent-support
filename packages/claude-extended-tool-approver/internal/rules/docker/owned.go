package docker

// Session-ownership guard for the destructive docker verbs (bead pg2-04xmm,
// design NG6 / D3 of docket pg2-a01hh; agent rule C-3).
//
// WHY. `docker rm|rmi|stop` and `docker volume rm` used to sit in the blanket
// safeSubcommands allowlist, so the hook auto-approved removing ANY container,
// image or volume on the engine, while agent rule C-3 ("MUST NOT stop, kill or
// remove a container it did not start, MUST NOT remove images or volumes it did
// not create") was prose only. This file is the mechanical half of C-3.
//
// WHAT COUNTS AS "THE SESSION'S OWN". The hook sees one command at a time, so
// ownership is PROVEN from the session transcript (HookInput.TranscriptPath):
// an object is the session's own when an EARLIER, SUCCESSFUL Bash tool call of
// this same session created it with a name or id the removal command repeats
// literally:
//
//   - containers: `docker run|create [--name N ...]` (also `docker container
//     run|create`), by name N, or by the container id the call printed;
//   - images:     `docker build -t|--tag T` (also `docker image build`,
//     `docker buildx build`), by tag T (an untagged ref means :latest);
//   - volumes:    `docker volume create [N]`, by name N.
//
// An object created by that call is the session's own whether or not the call
// also labeled it (`--label bead=<bead-id>`, `--label owner=<session-id>`, rule
// C-2): the label is how an OPERATOR sees the owner on the engine, the
// transcript is how the HOOK proves it. Anything the transcript cannot prove
// (a sibling's object, a label-only claim, a `$(docker ps -q ...)` expansion, a
// glob, an id prefix that matches nothing created here) is NOT auto-approved:
// the rule refuses (ADR 0044 floor, so a later rule cannot widen it) and Claude
// Code's own permission prompt decides.
//
// The prune verbs are never approved, with or without ownership.
//
// KNOWN LIMIT. A transcript tool_result carries ONE exit status per Bash call,
// so in `docker run --name a ...; docker run --name b ...` a failed first leaf
// followed by a successful last one is still counted as created. Only objects
// the agent itself named in its own commands can be claimed this way, so the
// worst case is the agent removing a name it asked to create.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// objectKind is the docker object class a guarded verb removes or stops.
type objectKind int

const (
	kindContainer objectKind = iota
	kindImage
	kindVolume
)

// guardedVerb is a recognised destructive docker verb. valueFlags lists the
// flags that consume the next argument; boolFlags the ones that do not. Any
// flag outside both is unknown, and an unknown flag refuses (it could carry
// semantics this rule has not modelled).
type guardedVerb struct {
	kind       objectKind
	valueFlags map[string]bool
	boolFlags  map[string]bool
}

func set(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

var (
	rmVerb     = guardedVerb{kind: kindContainer, boolFlags: set("-f", "--force", "-v", "--volumes", "-l", "--link")}
	stopVerb   = guardedVerb{kind: kindContainer, valueFlags: set("-t", "--time", "-s", "--signal")}
	rmiVerb    = guardedVerb{kind: kindImage, boolFlags: set("-f", "--force", "--no-prune")}
	volRmVerb  = guardedVerb{kind: kindVolume, boolFlags: set("-f", "--force")}
	pruneScope = set("system", "container", "image", "volume", "network", "builder", "buildx")
)

// classifyGuarded reports which guarded verb `args` (everything after
// `docker`) invokes and the args that follow the verb tokens. A global flag
// before the subcommand (`docker -H tcp://x rm c`, `--context prod`) does not
// match: args[0] is then a flag, and the caller refuses it separately, since
// such a command aims the removal at an engine whose objects this session never
// created.
func classifyGuarded(args []string) (verb guardedVerb, rest []string, matched bool) {
	if len(args) == 0 {
		return guardedVerb{}, nil, false
	}
	switch args[0] {
	case "rm":
		return rmVerb, args[1:], true
	case "stop":
		return stopVerb, args[1:], true
	case "rmi":
		return rmiVerb, args[1:], true
	case "container":
		if len(args) >= 2 {
			switch args[1] {
			case "rm", "remove":
				return rmVerb, args[2:], true
			case "stop":
				return stopVerb, args[2:], true
			}
		}
	case "image":
		if len(args) >= 2 && (args[1] == "rm" || args[1] == "remove") {
			return rmiVerb, args[2:], true
		}
	case "volume":
		if len(args) >= 2 && (args[1] == "rm" || args[1] == "remove") {
			return volRmVerb, args[2:], true
		}
	}
	return guardedVerb{}, nil, false
}

// isPrune reports a `docker [scope] prune` invocation (system, container,
// image, volume, network, builder prune).
func isPrune(args []string) bool {
	return len(args) >= 2 && pruneScope[args[0]] && args[1] == "prune"
}

// literalTarget matches a plain container/image/volume reference. It excludes
// every shell expansion character, so `$(...)`, backticks, globs and variables
// can never be mistaken for a name the transcript could prove.
var literalTarget = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/@-]*$`)

// parseGuardedTargets splits a guarded verb's args into its literal targets.
// ok is false when a flag is unknown, a target is not a literal reference, or
// there is no target at all.
func parseGuardedTargets(v guardedVerb, rest []string) (targets []string, ok bool) {
	i := 0
	for i < len(rest) {
		a := rest[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		switch {
		case v.boolFlags[a]:
			i++
		case v.valueFlags[a]:
			if i+1 >= len(rest) {
				return nil, false
			}
			i += 2
		case strings.Contains(a, "=") && v.valueFlags[a[:strings.IndexByte(a, '=')]]:
			i++
		default:
			return nil, false
		}
	}
	for ; i < len(rest); i++ {
		if !literalTarget.MatchString(rest[i]) {
			return nil, false
		}
		targets = append(targets, rest[i])
	}
	return targets, len(targets) > 0
}

// ownedObjects is what the session's transcript proves it created.
type ownedObjects struct {
	containerNames map[string]bool
	containerIDs   []string // full 64-hex ids printed by successful run/create
	images         map[string]bool
	volumes        map[string]bool
}

func newOwnedObjects() *ownedObjects {
	return &ownedObjects{containerNames: map[string]bool{}, images: map[string]bool{}, volumes: map[string]bool{}}
}

// normalizeImage makes an untagged reference mean :latest, as docker does.
func normalizeImage(ref string) string {
	slash := strings.LastIndexByte(ref, '/')
	if !strings.Contains(ref[slash+1:], ":") && !strings.Contains(ref, "@") {
		return ref + ":latest"
	}
	return ref
}

var hexID = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

func (o *ownedObjects) owns(kind objectKind, target string) bool {
	switch kind {
	case kindContainer:
		if o.containerNames[target] {
			return true
		}
		if hexID.MatchString(target) {
			for _, id := range o.containerIDs {
				if strings.HasPrefix(id, target) {
					return true
				}
			}
		}
		return false
	case kindImage:
		return o.images[normalizeImage(target)]
	case kindVolume:
		return o.volumes[target]
	}
	return false
}

// recordCreation folds one SUCCESSFUL docker leaf (args after `docker`) into o.
// stdout is the call's captured output (container ids for run -d / create).
func (o *ownedObjects) recordCreation(args []string, stdout string) {
	if len(args) == 0 {
		return
	}
	verb, rest := args[0], args[1:]
	if (verb == "container" || verb == "image" || verb == "buildx") && len(rest) > 0 {
		verb, rest = rest[0], rest[1:]
	}
	switch verb {
	case "run", "create":
		if name := flagValue(rest, "--name"); name != "" && literalTarget.MatchString(name) {
			o.containerNames[name] = true
		}
		for _, line := range strings.Split(stdout, "\n") {
			line = strings.TrimSpace(line)
			if len(line) == 64 && hexID.MatchString(line) {
				o.containerIDs = append(o.containerIDs, line)
			}
		}
	case "build":
		for _, tag := range flagValues(rest, "-t", "--tag") {
			if literalTarget.MatchString(tag) {
				o.images[normalizeImage(tag)] = true
			}
		}
	case "volume":
		if len(rest) > 0 && rest[0] == "create" {
			vargs := rest[1:]
			if name := flagValue(vargs, "--name"); name != "" && literalTarget.MatchString(name) {
				o.volumes[name] = true
			} else if pos := lastPositional(vargs); pos != "" && literalTarget.MatchString(pos) {
				o.volumes[pos] = true
			}
		}
	}
}

func flagValue(args []string, flag string) string {
	if v := flagValues(args, flag); len(v) > 0 {
		return v[0]
	}
	return ""
}

// flagValues returns every value of any of `flags`, in `--flag V` and
// `--flag=V` spellings.
func flagValues(args []string, flags ...string) []string {
	var out []string
	for i, a := range args {
		for _, f := range flags {
			switch {
			case a == f && i+1 < len(args):
				out = append(out, args[i+1])
			case strings.HasPrefix(a, f+"="):
				out = append(out, strings.TrimPrefix(a, f+"="))
			}
		}
	}
	return out
}

// lastPositional returns the final non-flag arg of `docker volume create`.
func lastPositional(args []string) string {
	valued := set("--driver", "-d", "--label", "--opt", "-o", "--name")
	pos := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if valued[a] {
				i++
			}
			continue
		}
		pos = a
	}
	return pos
}

// transcriptLine is the slice of a Claude Code transcript JSONL record this
// package reads. Only `assistant` (tool_use) and `user` (tool_result) lines
// matter.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// resultText flattens a tool_result's content (a string, or an array of
// {type:"text",text} blocks).
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, bl := range blocks {
		b.WriteString(bl.Text)
		b.WriteByte('\n')
	}
	return b.String()
}

// loadOwned reads the transcript at path and returns what the session
// successfully created. A tool call counts only when its tool_result is not an
// error: a `docker run --name victim` that failed on a name clash created
// nothing, and must not launder someone else's object into "owned".
// A missing or unreadable transcript yields an empty set (nothing proven).
func loadOwned(path string) *ownedObjects {
	if path == "" {
		return newOwnedObjects()
	}
	f, err := os.Open(path)
	if err != nil {
		return newOwnedObjects()
	}
	defer f.Close()
	return readOwned(f)
}

func readOwned(r io.Reader) *ownedObjects {
	o := newOwnedObjects()
	pending := map[string][][]string{} // tool_use id -> docker leaves' args
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			foldTranscriptLine(line, o, pending)
		}
		if err != nil {
			break
		}
	}
	return o
}

func foldTranscriptLine(line []byte, o *ownedObjects, pending map[string][][]string) {
	var tl transcriptLine
	if json.Unmarshal(line, &tl) != nil || (tl.Type != "assistant" && tl.Type != "user") {
		return
	}
	var blocks []contentBlock
	if json.Unmarshal(tl.Message.Content, &blocks) != nil {
		return
	}
	for _, b := range blocks {
		switch {
		case tl.Type == "assistant" && b.Type == "tool_use" && b.Name == "Bash":
			var in struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(b.Input, &in) != nil || !strings.Contains(in.Command, "docker") {
				continue
			}
			for _, leaf := range cmdparse.Parse(in.Command) {
				if baseName(leaf.Executable) == "docker" {
					pending[b.ID] = append(pending[b.ID], leaf.Args)
				}
			}
		case tl.Type == "user" && b.Type == "tool_result":
			leaves, ok := pending[b.ToolUseID]
			if !ok {
				continue
			}
			delete(pending, b.ToolUseID)
			if b.IsError {
				continue
			}
			out := resultText(b.Content)
			for _, args := range leaves {
				o.recordCreation(args, out)
			}
		}
	}
}

func baseName(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
