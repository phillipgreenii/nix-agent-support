// Package classify is Tier 2 of the mistake census (bead pg2-oisvb): it decides,
// for each Tier 1 candidate, whether a mistake or a correction actually happened,
// which class it is, and what would have prevented it.
//
// # Why this tier is semantic and not lexical
//
// Because lexical was measured and failed. Over every stored human turn — 1,209 of
// them — the patterns `i said`, `why did you`, `you should have` and `not what i`
// match a combined 3 turns, 0.25%, against 15 corrections in a 63-entry gold set.
// Tier 1 is therefore built entirely on structure, and the judgement Tier 1
// cannot make — "was this typed turn a correction, or the next instruction?" —
// needs to read the prose. Tier 1's whole purpose is to shrink that reading job
// from 405,986 events to ~2,100 candidates so it is affordable to do properly.
//
// # Why the engine is in-house
//
// See docs/adr/0045. AgentDebugX and IBM Agentic CLEAR were evaluated as the Tier
// 2/3 engine and declined: AgentDebugX explicitly does not detect user feedback or
// external corrections, which is the half this census exists for, and adopting it
// would put a pip/Python dependency inside a Go module on the gomod2nix engine in a
// nix flake. Their TAXONOMIES were adopted instead, at zero dependency cost — the
// class vocabulary below is MAST's three categories (specification issues,
// inter-agent misalignment, task verification) mapped onto what this corpus can
// actually evidence.
//
// # The two classifiers, and why both ship
//
// Baseline is the bar the semantic classifier must clear, not a fallback. It
// implements "every typed turn following a tool call is a correction" — the naive
// rule the bead names — so "the LLM is better" is a measurement rather than an
// assumption. CLI is the semantic classifier. It shells out to the Claude Code CLI
// already on this machine rather than adding an HTTP client and an API-key surface,
// and it reports what the run cost because an unbounded classification pass over a
// growing corpus is the obvious way this tooling becomes something nobody runs.
package classify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-ccaudit/internal/candidate"
)

// Class is what Tier 2 decides went wrong.
//
// The vocabulary is deliberately small and mutually exclusive: a taxonomy an
// evaluator cannot label consistently produces precision figures that measure the
// evaluator, not the classifier. MAST's categories are cited per class.
type Class string

const (
	// ClassNotAMistake is the majority verdict and must stay available: Tier 1 is
	// tuned for recall, so most of what it returns is ordinary work — the next
	// instruction, a legitimate `git reset` during a rebase, three passes over a
	// file because the change genuinely had three parts.
	ClassNotAMistake Class = "not-a-mistake"
	// ClassUserCorrection is the strongest evidence in the corpus: a person spent
	// attention catching something no exit code, schema or hook caught.
	ClassUserCorrection Class = "user-correction"
	// ClassSelfCaught is the agent noticing and repairing its own error. Cheaper —
	// one round trip rather than a person's attention — but still waste.
	ClassSelfCaught Class = "self-caught-mistake"
	// ClassSpecificationMiss is MAST's "specification issues": the agent had the
	// instruction and did not follow it.
	ClassSpecificationMiss Class = "specification-miss"
	// ClassVerificationMiss is MAST's "task verification": work asserted complete
	// that was not, e.g. a completion claim followed by a failing gate.
	ClassVerificationMiss Class = "verification-miss"
	// ClassGuidanceDefect is the inverted case and the one most worth finding: the
	// artifact meant to PREVENT errors caused them. The worked example is real — a
	// stored memory recommended `--no-ext-diff` without stating flag position, so
	// agents wrote `git --no-ext-diff diff`, which is invalid, 9 times in 8 days.
	// Routing such a finding forward to a new rule would be wrong; it routes BACK
	// to the instruction that induced it.
	ClassGuidanceDefect Class = "guidance-defect"
	// ClassPermissionFriction is the action being right and the approver refusing
	// it. Not an instruction problem, and a rule proposed for it is wasted.
	ClassPermissionFriction Class = "permission-friction"
	// ClassToolingDefect is infrastructure: a flaky service, an unavailable model,
	// a broken tool. Nothing in the agent's instructions can fix it.
	ClassToolingDefect Class = "tooling-defect"
)

// Classes is every class, in reporting order.
var Classes = []Class{
	ClassUserCorrection,
	ClassSelfCaught,
	ClassSpecificationMiss,
	ClassVerificationMiss,
	ClassGuidanceDefect,
	ClassPermissionFriction,
	ClassToolingDefect,
	ClassNotAMistake,
}

// ParseClass validates a label.
func ParseClass(s string) (Class, error) {
	for _, c := range Classes {
		if Class(strings.TrimSpace(strings.ToLower(s))) == c {
			return c, nil
		}
	}
	return "", fmt.Errorf("unknown class %q (want one of %s)", s, joinClasses())
}

func joinClasses() string {
	parts := make([]string, 0, len(Classes))
	for _, c := range Classes {
		parts = append(parts, string(c))
	}
	return strings.Join(parts, ", ")
}

// IsMistake reports whether the class describes something that went wrong.
func (c Class) IsMistake() bool { return c != ClassNotAMistake }

// IsCorrection reports whether a PERSON had to intervene. This is the binary the
// Baseline classifier predicts, and therefore the axis criterion 4's comparison
// runs on.
func (c Class) IsCorrection() bool { return c == ClassUserCorrection }

// Classification is one candidate's verdict.
type Classification struct {
	Candidate candidate.Candidate `json:"candidate"`
	Class     Class               `json:"class"`
	// Confidence is the classifier's own hedge: low|medium|high. A low-confidence
	// mistake verdict is reported but ranks below a high-confidence one.
	Confidence string `json:"confidence"`
	// What the agent did wrong, in the classifier's words.
	What string `json:"what"`
	// Prevention is what would have stopped it. Criterion 3 requires this on every
	// classification: a finding with no stated prevention cannot be routed, and an
	// unroutable finding is the failure mode Tier 3 exists to eliminate.
	Prevention string `json:"prevention"`
	// RouteHint is the classifier's suggestion. Tier 3 decides; this is advisory.
	RouteHint string `json:"route_hint"`
}

// Cost is what a classification pass consumed. Reported always, because criterion
// 3 requires the run cost to be BOUNDED and REPORTED, and because a pass whose
// cost nobody sees is a pass that quietly grows with the corpus.
type Cost struct {
	Classifier          string  `json:"classifier"`
	CandidatesIn        int     `json:"candidates_in"`
	ClassificationsOut  int     `json:"classifications_out"`
	Batches             int     `json:"batches"`
	Calls               int     `json:"calls"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	USD                 float64 `json:"usd"`
	// Elapsed is a time.Duration, so it marshals as NANOSECONDS. The tag says so
	// rather than saying `elapsed_ms`, which would name a unit the value does not
	// carry — the whole point of this package is that a number means what it says.
	Elapsed time.Duration `json:"elapsed_ns"`
	// Truncated is set when a --max bound cut the candidate set. It MUST be
	// reported: every rate computed downstream is over the truncated set, and a
	// silently partial pass presented as a census is the miscount this whole index
	// exists to prevent.
	Truncated bool `json:"truncated"`
}

// Line is the one-line cost summary, flat key=value so a supervising agent can
// assert on it without a parser (the same convention as ingest's summary).
func (c Cost) Line() string {
	return fmt.Sprintf(
		"classify complete: classifier=%s candidates_in=%d classifications_out=%d batches=%d calls=%d "+
			"input_tokens=%d output_tokens=%d cache_read=%d cache_creation=%d usd=%.4f truncated=%t elapsed=%s",
		c.Classifier, c.CandidatesIn, c.ClassificationsOut, c.Batches, c.Calls,
		c.InputTokens, c.OutputTokens, c.CacheReadTokens, c.CacheCreationTokens,
		c.USD, c.Truncated, c.Elapsed.Round(time.Millisecond),
	)
}

// Result is a classification pass.
type Result struct {
	Classifications []Classification `json:"classifications"`
	Cost            Cost             `json:"cost"`
}

// Classifier is Tier 2's seam. Two implementations ship and a third is injected by
// tests; nothing else in the census knows which one ran.
type Classifier interface {
	Name() string
	Classify(ctx context.Context, cands []candidate.Candidate) (Result, error)
}

// ── Baseline ────────────────────────────────────────────────────────────────────

// Baseline implements the rule criterion 4 names as the bar: "every typed turn
// following a tool call is a correction".
//
// It ships rather than being written once in a test because a bar that is not
// runnable is a bar nobody re-checks. Its numbers on the gold set are what make
// "the semantic classifier is worth its cost" a measurement.
type Baseline struct{}

// Name implements Classifier.
func (Baseline) Name() string { return "baseline" }

// Classify implements Classifier. Zero calls, zero cost, deterministic.
func (b Baseline) Classify(_ context.Context, cands []candidate.Candidate) (Result, error) {
	start := time.Now()
	out := make([]Classification, 0, len(cands))
	for _, c := range cands {
		cl := Classification{
			Candidate:  c,
			Class:      ClassNotAMistake,
			Confidence: "low",
			What:       "",
			Prevention: "",
		}
		if c.Signal == candidate.TypedTurn && c.Detail["prev_tool_seq"] != "" {
			cl.Class = ClassUserCorrection
			cl.What = "a human turn followed a tool call"
			cl.Prevention = "unknown — the baseline rule reads no prose, so it cannot say"
		}
		out = append(out, cl)
	}
	return Result{
		Classifications: out,
		Cost: Cost{
			Classifier:         b.Name(),
			CandidatesIn:       len(cands),
			ClassificationsOut: len(out),
			Elapsed:            time.Since(start),
		},
	}, nil
}

// ── CLI (semantic) ──────────────────────────────────────────────────────────────

// DefaultBatch is how many candidates go into one model call.
//
// Batching is a cost decision with a measured basis: one `claude -p` invocation
// re-primes the Claude Code system prompt, measured at 21,882 cache-creation
// tokens and $0.046 for a one-word reply, so the per-CALL overhead dwarfs the
// per-candidate content. Ten candidates per call amortises that overhead by an
// order of magnitude while keeping each call's output small enough to stay
// reliably parseable.
const DefaultBatch = 10

// EnvCommand overrides the classifier command line, space separated. It exists so
// the model can be pinned by machine configuration rather than by editing Go.
const EnvCommand = "PG_CCAUDIT_CLASSIFY_CMD"

// DefaultCommand is the shipped classifier invocation.
//
// `claude -p` rather than a direct HTTP client: the binary is already declared and
// managed by this flake, so there is no new dependency, no API-key handling, and
// no second place where a model id is configured. `--output-format json` is what
// makes the cost REPORTABLE — the envelope carries total_cost_usd and usage — and
// `--max-turns 1` keeps a classification from becoming an agent session.
func DefaultCommand() []string {
	if v := strings.TrimSpace(os.Getenv(EnvCommand)); v != "" {
		return strings.Fields(v)
	}
	return []string{
		"claude", "-p",
		"--model", "claude-haiku-4-5",
		"--output-format", "json",
		"--max-turns", "1",
	}
}

// Runner executes one classifier call. Injected by tests so the suite never needs
// a model, a network, or a credential.
type Runner func(ctx context.Context, argv []string, stdin string) ([]byte, error)

// CLI is the semantic classifier.
type CLI struct {
	Command []string
	Batch   int
	Run     Runner
}

// NewCLI builds the shipped semantic classifier.
func NewCLI() *CLI {
	return &CLI{Command: DefaultCommand(), Batch: DefaultBatch, Run: execRunner}
}

// Name implements Classifier.
func (c *CLI) Name() string {
	if len(c.Command) == 0 {
		return "cli"
	}
	return "cli:" + c.Command[0]
}

func execRunner(ctx context.Context, argv []string, stdin string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty classifier command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w: %s", argv[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// envelope is the `claude -p --output-format json` result object. Only the fields
// this package needs are decoded.
type envelope struct {
	Result       string  `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	IsError      bool    `json:"is_error"`
	Usage        struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// verdict is one element of the model's JSON reply.
type verdict struct {
	ID         string `json:"id"`
	Class      string `json:"class"`
	Confidence string `json:"confidence"`
	What       string `json:"what"`
	Prevention string `json:"prevention"`
	Route      string `json:"route"`
}

// CandidateID is the stable identity of a candidate across a Tier 1 run, a
// classification pass and a gold set.
//
// It is candidate.Key — (signal, path, seq) plus a uniqueness ordinal where a
// signal legitimately emits two rows for one line. A positional index into a slice
// would break the moment a query's ORDER BY or a window changed, and the bare
// triple was measured to collide.
func CandidateID(c candidate.Candidate) string {
	if c.Key != "" {
		return c.Key
	}
	return fmt.Sprintf("%s:%s#%d", c.Signal, c.Path, c.Seq)
}

// Classify implements Classifier.
func (c *CLI) Classify(ctx context.Context, cands []candidate.Candidate) (Result, error) {
	start := time.Now()
	batch := c.Batch
	if batch <= 0 {
		batch = DefaultBatch
	}
	cost := Cost{Classifier: c.Name(), CandidatesIn: len(cands)}
	byID := make(map[string]candidate.Candidate, len(cands))
	for _, cd := range cands {
		byID[CandidateID(cd)] = cd
	}

	var out []Classification
	for i := 0; i < len(cands); i += batch {
		end := i + batch
		if end > len(cands) {
			end = len(cands)
		}
		chunk := cands[i:end]
		cost.Batches++

		prompt, err := renderPrompt(chunk)
		if err != nil {
			return Result{}, err
		}
		raw, err := c.Run(ctx, c.Command, prompt)
		cost.Calls++
		if err != nil {
			return Result{Classifications: out, Cost: cost}, fmt.Errorf("classifier call %d: %w", cost.Calls, err)
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return Result{Classifications: out, Cost: cost},
				fmt.Errorf("classifier call %d: decode envelope: %w", cost.Calls, err)
		}
		cost.USD += env.TotalCostUSD
		cost.InputTokens += env.Usage.InputTokens
		cost.OutputTokens += env.Usage.OutputTokens
		cost.CacheReadTokens += env.Usage.CacheReadInputTokens
		cost.CacheCreationTokens += env.Usage.CacheCreationInputTokens
		if env.IsError {
			return Result{Classifications: out, Cost: cost},
				fmt.Errorf("classifier call %d reported an error: %s", cost.Calls, excerpt(env.Result))
		}

		verdicts, err := parseVerdicts(env.Result)
		if err != nil {
			return Result{Classifications: out, Cost: cost},
				fmt.Errorf("classifier call %d: %w", cost.Calls, err)
		}
		for _, v := range verdicts {
			cd, ok := byID[v.ID]
			if !ok {
				// A verdict for a candidate that was not sent is dropped rather than
				// attached to the wrong row. Silently mis-attributing a verdict would
				// corrupt precision and recall in a way nothing downstream could see.
				continue
			}
			cl, err := ParseClass(v.Class)
			if err != nil {
				return Result{Classifications: out, Cost: cost},
					fmt.Errorf("classifier call %d, candidate %s: %w", cost.Calls, v.ID, err)
			}
			out = append(out, Classification{
				Candidate:  cd,
				Class:      cl,
				Confidence: normalizeConfidence(v.Confidence),
				What:       strings.TrimSpace(v.What),
				Prevention: strings.TrimSpace(v.Prevention),
				RouteHint:  strings.TrimSpace(v.Route),
			})
		}
	}
	cost.ClassificationsOut = len(out)
	cost.Elapsed = time.Since(start)
	return Result{Classifications: out, Cost: cost}, nil
}

func normalizeConfidence(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "high":
		return "high"
	case "medium", "med":
		return "medium"
	default:
		return "low"
	}
}

func excerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		return s[:197] + "..."
	}
	return s
}

// parseVerdicts extracts the JSON array from the model's reply.
//
// It tolerates a fenced block and surrounding prose because a model that has been
// told to emit bare JSON sometimes does not, and failing the whole batch over a
// pair of backticks would throw away a paid call.
func parseVerdicts(reply string) ([]verdict, error) {
	s := strings.TrimSpace(reply)
	if i := strings.Index(s, "```"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "\n"); j >= 0 {
			rest = rest[j+1:]
		}
		if k := strings.Index(rest, "```"); k >= 0 {
			rest = rest[:k]
		}
		s = strings.TrimSpace(rest)
	}
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON array in classifier reply: %s", excerpt(reply))
	}
	var out []verdict
	if err := json.Unmarshal([]byte(s[start:end+1]), &out); err != nil {
		return nil, fmt.Errorf("decode classifier reply: %w: %s", err, excerpt(reply))
	}
	return out, nil
}
