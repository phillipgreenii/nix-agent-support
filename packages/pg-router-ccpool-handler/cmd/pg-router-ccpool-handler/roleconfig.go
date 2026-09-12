package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/config"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/prompt"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// envRoleConfig/envConfig name this module's OWN configuration inputs — a
// per-process role (this handler process dispatches ONE role, registered
// under its own `register --id`; DEC-WIRE-1's register message names the
// participant, not a per-dispatch role selection) and the launch/prompt
// Config. A fuller config story (this module's own TOML file, decoded like
// packages/pg-router/internal/config/registry.go decodes pg-router's own
// [[role]] table) is docket pg2-oju6w's Task 5.7, not this one's — these
// small JSON files are this packet's minimum viable equivalent, sufficient
// to make dispatch real without inventing Task 5.7's design.
const (
	envRoleConfig = "PG_ROUTER_CCPOOL_HANDLER_ROLE"
	envConfig     = "PG_ROUTER_CCPOOL_HANDLER_CONFIG"
)

// roleFile is the on-disk JSON shape --role-config decodes. Completion/
// FailureAction/DispatchFailAction already implement encoding.TextUnmarshaler
// (internal/roles/enums.go), so encoding/json calls those directly for the
// matching string fields — only PromptBody/Argv need a second pass to parse
// into internal/prompt's *template.Template form roles.CCPoolConfig/
// CommandConfig actually carry.
type roleFile struct {
	Name   string `json:"name"`
	Type   string `json:"type"` // "ccpool" | "command"
	CCPool *struct {
		Actor           string                   `json:"actor"`
		SkillMD         string                   `json:"skillMD"`
		Completion      roles.Completion         `json:"completion"`
		OnFailure       roles.FailureAction      `json:"onFailure"`
		OnDispatchFail  roles.DispatchFailAction `json:"onDispatchFail"`
		AuthorshipGuard bool                     `json:"authorshipGuard"`
		PromptBody      string                   `json:"promptBody"`
		Isolation       roles.IsolationConfig    `json:"isolation"`
	} `json:"ccpool,omitempty"`
	Command *struct {
		Argv []string `json:"argv"`
	} `json:"command,omitempty"`
}

// loadRole reads and decodes a roleFile from path into a roles.Role, parsing
// its prompt/argv templates. An empty path is a configuration error — every
// dispatch invocation needs to know which role it is.
func loadRole(path string) (roles.Role, error) {
	if path == "" {
		return roles.Role{}, fmt.Errorf("no role config given (--role-config, or " + envRoleConfig + ")")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return roles.Role{}, fmt.Errorf("read role config %s: %w", path, err)
	}
	var rf roleFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return roles.Role{}, fmt.Errorf("decode role config %s: %w", path, err)
	}
	r := roles.Role{Name: rf.Name, Type: rf.Type}
	switch rf.Type {
	case "ccpool":
		if rf.CCPool == nil {
			return roles.Role{}, fmt.Errorf("role config %s: type is ccpool but no [ccpool] block", path)
		}
		tmpl, err := prompt.Parse(rf.Name, rf.CCPool.PromptBody)
		if err != nil {
			return roles.Role{}, fmt.Errorf("role config %s: parse promptBody: %w", path, err)
		}
		r.CCPool = &roles.CCPoolConfig{
			Actor: rf.CCPool.Actor, SkillMD: rf.CCPool.SkillMD,
			Completion: rf.CCPool.Completion, OnFailure: rf.CCPool.OnFailure, OnDispatchFail: rf.CCPool.OnDispatchFail,
			AuthorshipGuard: rf.CCPool.AuthorshipGuard, PromptBody: rf.CCPool.PromptBody, Prompt: tmpl,
			Isolation: rf.CCPool.Isolation,
		}
	case "command":
		if rf.Command == nil {
			return roles.Role{}, fmt.Errorf("role config %s: type is command but no [command] block", path)
		}
		// ArgvTmpl is left nil: commandRun.renderArgv (internal/executor/
		// command.go) re-parses each Argv element fresh via prompt.Parse at
		// dispatch time and never reads Role.Command.ArgvTmpl — that field
		// exists on packages/pg-router/internal/roles.CommandConfig purely
		// for config-load-time template validation, which this module's own
		// fuller config story (Task 5.7) may add later.
		r.Command = &roles.CommandConfig{Argv: rf.Command.Argv}
	default:
		return roles.Role{}, fmt.Errorf("role config %s: unknown type %q (want ccpool or command)", path, rf.Type)
	}
	return r, nil
}

// loadConfig reads and decodes the launch/prompt config.Config from path,
// overlaying config.Default() so an absent/partial file still yields sane
// values. An empty path uses config.Default() outright.
func loadConfig(path string) (config.Config, error) {
	c := config.Default()
	if path == "" {
		return c, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return config.Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	return c, nil
}
