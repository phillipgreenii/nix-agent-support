package rolefile

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/phillipgreenii/pg-router/internal/handlers/config"
	"github.com/phillipgreenii/pg-router/internal/handlers/roles"
)

// roleConfigPath is the path every problem about the role's config is reported
// under; the strict shape check names fields relative to it.
const roleConfigPath = "role.config"

// configKeys are the only top-level keys of a ccpool role's [role.config]: the
// ccpool-role fields, names unchanged. name, type, survivesShutdown
// and command are not config (the role's name is the request's
// role.name, survives_shutdown is a core key, and the others belong to the
// command adapter or to no adapter); each is an unknown key here. The match is
// case-insensitive, as encoding/json's is.
var configKeys = map[string]bool{"ccpool": true, "sessionenv": true}

// DecodeRoleObject strictly decodes ro.Config into a ccpool roles.Role and returns
// EVERY problem it finds, in a stable order: the strict shape (an unknown key
// or a wrong type anywhere, keys sorted), the sessionEnv key rules (one problem
// per offending key), then the load rules (actor required, templates parse,
// durations, effort, prefetch, ...). The role
// is meaningful only when problems is empty.
//
// The per-field shape and load rules are RoleFile and
// DecodeRole; this adapter only decides which top-level keys exist.
func DecodeRoleObject(ro RoleObject) (roles.Role, []string) {
	var problems []string
	known := map[string]any{}
	for _, k := range SortedKeys(ro.Config) {
		if !configKeys[strings.ToLower(k)] {
			problems = append(problems, fmt.Sprintf("%s: unknown key %q", roleConfigPath, k))
			continue
		}
		known[k] = ro.Config[k]
	}

	shape := ShapeChecker{}
	shape.Walk(roleConfigPath, known, reflect.TypeOf(RoleFile{}), nil)
	problems = append(problems, shape.Problems...)

	// sessionEnv keys: every offending key, not just the first (INV-CCH-33).
	for k, v := range known {
		if !strings.EqualFold(k, "sessionEnv") {
			continue
		}
		env, _ := v.(map[string]any)
		for _, key := range SortedKeys(env) {
			if err := ValidateSessionEnv(map[string]string{key: ""}); err != nil {
				problems = append(problems, roleConfigPath+".sessionEnv: "+err.Error())
			}
		}
	}

	hasCCPool := false
	for k := range known {
		hasCCPool = hasCCPool || strings.EqualFold(k, "ccpool")
	}
	if !hasCCPool {
		problems = append(problems, roleConfigPath+".ccpool: required (a role of this adapter launches a ccpool session)")
	}

	// The load rules need a decoded value; a type error means the typed decode
	// would only repeat it (or fail on the first one).
	if !shape.TypeError && hasCCPool {
		withName := make(map[string]any, len(known)+1)
		for k, v := range known {
			withName[k] = v
		}
		withName["name"] = ro.Name
		b, err := json.Marshal(withName)
		if err != nil { // unreachable: the config came out of a JSON decode
			return roles.Role{}, append(problems, fmt.Sprintf("%s: %v", roleConfigPath, err))
		}
		role, err := DecodeRole(roleConfigPath, b, false)
		if err != nil {
			// DecodeRole prefixes its message "role config <path>: "; report it under the config path like the other problems.
			problems = append(problems, roleConfigPath+": "+strings.TrimPrefix(err.Error(), "role config "+roleConfigPath+": "))
		} else if len(problems) == 0 {
			return role, nil
		}
	}
	return roles.Role{}, problems
}

// LoadRoleAndConfig decodes the request's role strictly and loads the
// handler-wide launch config, then applies the checks that need both and the
// pool-wide budget overlay. It is the preparation dispatch and postStartup share;
// the error is the message the caller reports.
func LoadRoleAndConfig(ro RoleObject, cfgPath string) (roles.Role, config.Config, error) {
	role, problems := DecodeRoleObject(ro)
	if len(problems) > 0 {
		return roles.Role{}, config.Config{}, fmt.Errorf("role %q: invalid config: %s", ro.Name, strings.Join(problems, "; "))
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return roles.Role{}, config.Config{}, err
	}
	// The bounds that depend on the handler-wide config (poll interval, quiet
	// window maximum) can only be checked here, where both are loaded.
	if err := cfg.ValidateQuietWindowOverride(role.CCPool.WorktreeQuietWindow); err != nil {
		return roles.Role{}, config.Config{}, fmt.Errorf("role %s: %v", role.Name, err)
	}
	if err := cfg.ValidateNeedsInputStall(role.CCPool.NeedsInputStall); err != nil {
		return roles.Role{}, config.Config{}, fmt.Errorf("role %s: needsInputStall: %v", role.Name, err)
	}
	OverlayBudgetThresholds(role, cfg)
	return role, cfg, nil
}
