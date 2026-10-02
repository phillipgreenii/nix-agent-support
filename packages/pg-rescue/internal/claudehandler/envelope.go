package claudehandler

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/pg-rescue/internal/contract"
)

// Error kinds recorded in meta.error_kind when the handler exits 1 without a
// usable agent verdict.
const (
	kindTimeLimit  = "time_limit"
	kindSpawn      = "spawn"
	kindCLIError   = "cli_error"
	kindBadOutput  = "bad_cli_output"
	kindUnparsable = "unparseable_result"
)

// envelope is the part of `claude -p --output-format json`'s single result
// object this handler reads. Fields are pointers or raw messages where the
// handler must tell "absent" from "zero", because meta only carries what the
// CLI actually reported.
type envelope struct {
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	// Result is the model's final text. With --json-schema the CLI puts the
	// validated object in StructuredOutput instead and may leave Result empty.
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	SessionID        string          `json:"session_id"`
	TotalCostUSD     *float64        `json:"total_cost_usd"`
	NumTurns         *int            `json:"num_turns"`
	Usage            json.RawMessage `json:"usage"`
	Model            string          `json:"model"`
	ModelUsage       map[string]struct {
		CostUSD float64 `json:"costUSD"`
	} `json:"modelUsage"`
	PermissionDenials []struct {
		ToolName string `json:"tool_name"`
	} `json:"permission_denials"`
	TerminalReason string `json:"terminal_reason"`
}

// parseEnvelope decodes the CLI's stdout as the result object.
func parseEnvelope(stdout []byte) (*envelope, error) {
	var e envelope
	if err := json.Unmarshal(stdout, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// finalMessage returns the agent's final message. With --json-schema the CLI
// reports the validated object as structured_output; otherwise (or when it
// does not) the model's text is result.
func (e *envelope) finalMessage() string {
	so := strings.TrimSpace(string(e.StructuredOutput))
	if so != "" && so != "null" {
		return so
	}
	return e.Result
}

// model names the model that did the work: a top-level "model" if the CLI
// ever reports one, otherwise the modelUsage entry that cost the most (ties
// broken by name, so the answer is stable).
func (e *envelope) model() string {
	if e.Model != "" {
		return e.Model
	}
	names := make([]string, 0, len(e.ModelUsage))
	for n := range e.ModelUsage {
		names = append(names, n)
	}
	sort.Strings(names)
	best, bestCost := "", -1.0
	for _, n := range names {
		if c := e.ModelUsage[n].CostUSD; c > bestCost {
			best, bestCost = n, c
		}
	}
	return best
}

// meta is what this handler adds to its result: the CLI's session id, cost,
// turn count, usage and model, plus the names of any tools the CLI denied.
// Only fields the CLI reported appear. e may be nil (no envelope).
func (e *envelope) meta() map[string]any {
	m := map[string]any{}
	if e == nil {
		return m
	}
	if e.SessionID != "" {
		m["session_id"] = e.SessionID
	}
	if e.TotalCostUSD != nil {
		m["total_cost_usd"] = *e.TotalCostUSD
	}
	if e.NumTurns != nil {
		m["num_turns"] = *e.NumTurns
	}
	if u := strings.TrimSpace(string(e.Usage)); u != "" && u != "null" {
		m["usage"] = e.Usage
	}
	if model := e.model(); model != "" {
		m["model"] = model
	}
	if len(e.PermissionDenials) > 0 {
		tools := make([]string, len(e.PermissionDenials))
		for i, d := range e.PermissionDenials {
			tools[i] = d.ToolName
		}
		m["permission_denials"] = tools
	}
	return m
}

// verdict is the agent's parsed final message.
type verdict struct {
	Outcome contract.Outcome
	Summary string
	Details string
}

// interpret parses the agent's final message as the result object: exactly one
// JSON object naming an outcome (resolved, deferred or declined), with
// optional string summary and details. The result rules themselves are
// contract.Classify's; this only adds that an outcome is required. A message
// wrapped whole in one code fence is accepted, since models often add one.
func interpret(msg string) (verdict, error) {
	var firstErr error
	for _, cand := range candidates(msg) {
		v, err := interpretOne(cand)
		if err == nil {
			return v, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return verdict{}, firstErr
}

func interpretOne(msg string) (verdict, error) {
	if msg == "" {
		return verdict{}, fmt.Errorf("the final message is empty")
	}
	members, err := contract.ParseObject([]byte(msg))
	if err != nil {
		_, reason, _ := contract.Classify(contract.ExitResolved, []byte(msg), false)
		if reason == "" {
			reason = err.Error()
		}
		return verdict{}, fmt.Errorf("%s", reason)
	}
	raw, ok := members["outcome"]
	if !ok {
		return verdict{}, fmt.Errorf("the final message has no \"outcome\" field")
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		return verdict{}, fmt.Errorf("the \"outcome\" field must be a string")
	}
	want, err := contract.ParseReportable(name)
	if err != nil {
		return verdict{}, err
	}
	code, _ := want.ExitCode()
	got, reason, rep := contract.Classify(code, []byte(msg), false)
	if got != want {
		return verdict{}, fmt.Errorf("%s", reason)
	}
	return verdict{Outcome: got, Summary: rep.Summary, Details: rep.Details}, nil
}

// candidates are the readings of msg to try, in order: as given, then with one
// enclosing code fence removed.
func candidates(msg string) []string {
	trimmed := strings.TrimSpace(msg)
	out := []string{trimmed}
	if inner, ok := unfence(trimmed); ok {
		out = append(out, inner)
	}
	return out
}

// unfence removes a code fence that encloses all of s (an opening line of
// three or more backticks, optionally followed by a language, and a closing
// line of the same backticks).
func unfence(s string) (string, bool) {
	if !strings.HasPrefix(s, "```") {
		return "", false
	}
	first, rest, ok := strings.Cut(s, "\n")
	if !ok {
		return "", false
	}
	ticks := first[:len(first)-len(strings.TrimLeft(first, "`"))]
	rest = strings.TrimRight(rest, " \t\r\n")
	body, last := "", rest
	if i := strings.LastIndexByte(rest, '\n'); i >= 0 {
		body, last = rest[:i], rest[i+1:]
	}
	if strings.TrimSpace(last) != ticks {
		return "", false
	}
	return strings.TrimSpace(body), true
}
