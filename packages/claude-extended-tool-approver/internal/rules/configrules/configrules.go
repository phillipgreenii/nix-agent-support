package configrules

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// Config is the JSON structure read from
// $XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json.
type Config struct {
	ApprovedCommands []string `json:"approvedCommands"`
	BlockedCommands  []string `json:"blockedCommands"`
}

// Rule approves/blocks commands from a config file. Thread-safe after construction.
// Absent or malformed file: all inputs abstain.
type Rule struct {
	approved map[string]bool
	blocked  map[string]bool
}

// New constructs a Rule from the default XDG location:
// $XDG_CONFIG_HOME/claude-extended-tool-approver/rules.json
// Falls back to ~/.config/... when XDG_CONFIG_HOME is unset.
func New() *Rule {
	xdgCfg := os.Getenv("XDG_CONFIG_HOME")
	if xdgCfg == "" {
		home, _ := os.UserHomeDir()
		xdgCfg = filepath.Join(home, ".config")
	}
	return NewFromFile(filepath.Join(xdgCfg, "claude-extended-tool-approver", "rules.json"))
}

// NewFromFile constructs a Rule from an explicit path. Absent/malformed → no-op rule.
func NewFromFile(path string) *Rule {
	data, err := os.ReadFile(path)
	if err != nil {
		return &Rule{approved: map[string]bool{}, blocked: map[string]bool{}}
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return &Rule{approved: map[string]bool{}, blocked: map[string]bool{}}
	}
	r := &Rule{
		approved: make(map[string]bool, len(cfg.ApprovedCommands)),
		blocked:  make(map[string]bool, len(cfg.BlockedCommands)),
	}
	for _, cmd := range cfg.ApprovedCommands {
		r.approved[cmd] = true
	}
	for _, cmd := range cfg.BlockedCommands {
		r.blocked[cmd] = true
	}
	return r
}

func (r *Rule) Name() string { return "config-rules" }

func (r *Rule) Evaluate(input *hookio.HookInput) hookio.RuleResult {
	if input.ToolName != "Bash" {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	cmdStr, err := input.BashCommand()
	if err != nil {
		return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
	}
	parsed := cmdparse.Parse(cmdStr)
	for _, pc := range parsed {
		base := filepath.Base(pc.Executable)
		if r.blocked[base] {
			return hookio.RuleResult{
				Decision: hookio.Reject,
				Reason:   "config-rules: " + base + " is in blocked list",
				Module:   r.Name(),
			}
		}
		if r.approved[base] {
			return hookio.RuleResult{
				Decision: hookio.Approve,
				Reason:   "config-rules: " + base + " is in approved list",
				Module:   r.Name(),
			}
		}
	}
	return hookio.RuleResult{Decision: hookio.Abstain, Module: r.Name()}
}
