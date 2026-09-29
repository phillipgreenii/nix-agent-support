// Package goldencorpus defines the checked-in corpus format for the
// claude-extended-tool-approver (ceta) effect engine, and holds the seeded
// rows themselves (testdata/corpus.json).
//
// # Provenance
//
// This is docket tc-o14i5.1 ("Phase 0: Decisions, freeze, oracle")'s own
// packet 0.2 ("Checked-in corpus format + seed rows"): define the corpus
// format described by the docket design's P2 contract, and seed it with
// every confirmed hole from the 2026-09-25 review plus the named seed-row
// categories, so later work (this docket's sibling legacy-extraction
// packet, and later phases' engine validation) has a landing target format
// to write rows into.
//
// # Format
//
// The P2 corpus contract (docket design, verbatim): "every golden row
// carries an expected verdict in {approve, reject, not-approve}. not-approve
// = abstain or reject acceptable (a classifier approval after abstain is
// acceptable for not-approve rows; must-reject rows MUST be deny). A
// must-reject or not-approve row MUST NOT produce an EFFECTIVE allow: allow,
// or {} where a settings permissions.allow rule matches ... In plan mode the
// hook MUST NOT approve any write/exec effect." The corpus's own
// expected-verdict enum is exactly the three Verdict values below (see
// Verdict's own doc comment for why the design's "deny" is not a fourth
// value).
//
// Row's own field list is the corpus format fields named verbatim by the
// docket design (Phase 0 item 2): "tool input + cwd/path-state fixture +
// mode + settings fixture + rules.json/config fixture (vetted hosts, remote
// lifecycle, kube contexts, remote paths, build-tool verbs, target-spec
// fixture (P8)) => expected verdict (P2)."
//
// # Freedom exercised (docket design leaves this to the implementer)
//
// The on-disk directory/file layout and serialization format are this
// packet's own choice (see its "Freedom" section) — consistent with, but
// not identical to, the existing internal/effectpolicy/testdata/ golden-case
// convention (one command per named case, prior art this package's schema
// deliberately mirrors in spirit — see PathStateFixture's doc comment for
// the specific overlap with that package's fixture() helper). That
// convention pairs two hand-authored .mmd files per directory for a REAL
// engine already wired to render them; this packet has no consuming loader
// yet (that is later-phase work, per this packet's own Validation section),
// so a single checked-in JSON array (testdata/corpus.json) was chosen
// instead of one file/directory per row: it is far cheaper to author and
// review at this seeding stage, and a later phase that DOES wire a loader
// can re-shard it into per-case files then, if that shape turns out to
// matter once something actually consumes it.
//
// KubeContextRule/RemotePathRule/VerbScopedApproval below mirror
// internal/evalcontract's own same-named spike-local shapes field-for-field
// (not imported: this packet's own Files section is read-only against
// internal/evalcontract, and evalcontract's own doc comments repeatedly
// flag its shapes as "this spike's stand-in ... production wiring is a
// follow-up" — a corpus-schema package is a more stable long-term shape for
// a future loader to target than a spike package).
package goldencorpus

// Verdict is the corpus's fixed three-value expected-verdict vocabulary
// (P2 corpus contract). NotApprove is not a fourth value distinct from
// Reject — it means "abstain or reject both acceptable". A row uses Reject
// only when its own point is to pin the verdict at Reject specifically (so
// a future loader catches a regression to Approve OR a silent
// reject-to-abstain drift); every other must-not-approve row uses
// NotApprove.
type Verdict string

const (
	Approve    Verdict = "approve"
	Reject     Verdict = "reject"
	NotApprove Verdict = "not-approve"
)

// Valid reports whether v is one of the corpus's three recognised verdict
// values.
func (v Verdict) Valid() bool {
	switch v {
	case Approve, Reject, NotApprove:
		return true
	default:
		return false
	}
}

// Mode is the Claude Code permission mode in effect when the row's tool
// call is evaluated. The P2 corpus contract names plan mode specifically
// ("In plan mode the hook MUST NOT approve any write/exec effect"); this
// packet's own Validation section is structural only (no consuming
// loader/engine is wired to this format yet), and precedence/plan-mode
// semantics measurement is sibling packet "Phase 0.4"'s own scope, not
// this packet's — so this field is carried in the format now (no later
// migration needed to add it) without this packet itself seeding a
// dedicated plan-mode precedence row.
type Mode string

const (
	ModeDefault           Mode = "default"
	ModeAcceptEdits       Mode = "acceptEdits"
	ModePlan              Mode = "plan"
	ModeBypassPermissions Mode = "bypassPermissions"
)

// Valid reports whether m is one of the four Claude Code permission modes
// this format recognises.
func (m Mode) Valid() bool {
	switch m {
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeBypassPermissions:
		return true
	default:
		return false
	}
}

// ToolInputFixture is the "tool input" field: the Claude Code PreToolUse
// hook's own (tool_name, tool_input) pair, carried verbatim (not
// pre-parsed or normalised), so a future loader can feed a row straight to
// the real hook adapter (internal/claudecodeadapter) unchanged. ToolName is
// e.g. "Bash", "Write", "Skill", "Agent" — anything the hook can name; a
// non-shell tool row (Skill, Agent, Monitor, ToolSearch, AskUserQuestion,
// ScheduleWakeup, ListAgents, SendMessage) uses this same shape with its
// own tool-specific ToolInput keys.
type ToolInputFixture struct {
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
}

// PathStateFixture is the "cwd/path-state fixture" field: the filesystem
// facts a path-classifying policy needs beyond the raw command/tool-input
// text — the invocation's CWD, the project root it resolves under (empty
// means "same as CWD"), the resolved HOME (only set when a row's verdict
// depends on a home-relative path, e.g. an R7 home-equivalent row), and
// optional git-tracked/gitignored/worktree-state overrides for a row whose
// verdict depends on tracked-ness or worktree cleanliness. This mirrors, at
// the DATA level, internal/effectpolicy/golden_test.go's own fixture()
// helper (a throwaway project root plus a separate HOME, with git-tracked
// and worktree-state fakes) — that package builds its fixture as live Go
// closures against a real temp directory because it has a real engine to
// exercise; this corpus has no consuming loader yet, so the same facts are
// named as data instead, for a future loader to materialise.
type PathStateFixture struct {
	CWD           string   `json:"cwd"`
	ProjectRoot   string   `json:"project_root,omitempty"`
	Home          string   `json:"home,omitempty"`
	GitTracked    []string `json:"git_tracked,omitempty"`
	Gitignored    []string `json:"gitignored,omitempty"`
	WorktreeState string   `json:"worktree_state,omitempty"`
}

// SettingsFixture is the "settings fixture" field: the operator's Claude
// Code settings.json permissions block relevant to the row. Kept as its
// own field (rather than folded into RulesConfigFixture) because the P2
// corpus contract's own invariant — "a must-reject or not-approve row MUST
// NOT produce an EFFECTIVE allow ... a settings permissions.allow rule
// matches" — is exactly the shape a future loader must prove does NOT flip
// a reject/not-approve row to an effective allow; keeping it separate from
// the rules.json/config fixture makes that specific check a single-field
// read.
type SettingsFixture struct {
	PermissionsAllow []string `json:"permissions_allow,omitempty"`
	PermissionsDeny  []string `json:"permissions_deny,omitempty"`
	PermissionsAsk   []string `json:"permissions_ask,omitempty"`
}

// KubeContextRule is one kube context's operator-configured allow-list —
// see internal/evalcontract.KubeContextRule (this type mirrors it
// field-for-field; see this package's own doc comment for why it is a
// separate declaration rather than an import).
type KubeContextRule struct {
	Allow []string `json:"allow"`
}

// RemotePathRule is one categorized-path override entry — see
// internal/evalcontract.RemotePathRule (mirrored field-for-field).
type RemotePathRule struct {
	Prefix   string `json:"prefix"`
	Category string `json:"category"`
}

// VerbScopedApproval is one build-tool-family verb-scoped approval entry —
// see internal/evalcontract.VerbScopedApproval (mirrored field-for-field;
// Child is deliberately omitted here, exactly as evalcontract's own doc
// comment marks it reserved data no policy in that spike constructs or
// reads yet).
type VerbScopedApproval struct {
	Tool  string `json:"tool"`
	Verb  string `json:"verb"`
	Class string `json:"class,omitempty"`
}

// Target-spec classes (P8, docket design): a target-spec entry names a
// target (docker context/host, kube context/server, vault address, ssh
// host, git remote) and its operator-declared class. Mutation of a
// TargetClassProduction target rejects; of an unlisted target abstains; of
// TargetClassTrustedDev is judged by the effect itself.
const (
	TargetClassProduction = "production"
	TargetClassTrustedDev = "trusted-dev"
)

// TargetSpecEntry is one P8 target-spec fixture row (docket design, Phase 0
// item "P8"). P8's own migration note ("Existing rules.json
// kubeContexts/remoteLifecycle entries migrate into the target spec; the
// old keys remain for rollback") is why RulesConfigFixture below carries
// both KubeContexts/RemoteLifecycle AND TargetSpec rather than one
// replacing the other.
type TargetSpecEntry struct {
	Target string `json:"target"`
	Class  string `json:"class"`
}

// RulesConfigFixture is the "rules.json/config fixture" field, with its six
// named sub-parts verbatim from the P2 corpus contract's own field list:
// "vetted hosts, remote lifecycle, kube contexts, remote paths, build-tool
// verbs, target-spec fixture (P8)". All six are optional per row — most
// rows populate none of them (nil/empty means "no operator configuration",
// matching internal/evalcontract.Request's own documented default-abstain
// semantics for each of its mirrored fields).
type RulesConfigFixture struct {
	VettedHosts     []string                    `json:"vetted_hosts,omitempty"`
	RemoteLifecycle map[string]string           `json:"remote_lifecycle,omitempty"`
	KubeContexts    map[string]KubeContextRule  `json:"kube_contexts,omitempty"`
	RemotePaths     map[string][]RemotePathRule `json:"remote_paths,omitempty"`
	BuildToolVerbs  []VerbScopedApproval        `json:"build_tool_verbs,omitempty"`
	TargetSpec      []TargetSpecEntry           `json:"target_spec,omitempty"`
}

// Row is one seeded corpus entry: exactly the corpus format fields named by
// the P2 corpus contract, plus Case (a stable, unique name), Tags (which
// required seed-row categories this row counts toward — see corpus_test.go's
// category-coverage checks) and Notes (the row's own provenance/rationale,
// since no consuming loader exists yet to derive it from anywhere else).
type Row struct {
	Case            string             `json:"case"`
	Tags            []string           `json:"tags,omitempty"`
	ToolInput       ToolInputFixture   `json:"tool_input"`
	CWDPathState    PathStateFixture   `json:"cwd_path_state"`
	Mode            Mode               `json:"mode"`
	Settings        SettingsFixture    `json:"settings"`
	RulesConfig     RulesConfigFixture `json:"rules_config"`
	ExpectedVerdict Verdict            `json:"expected_verdict"`
	Notes           string             `json:"notes,omitempty"`
}
