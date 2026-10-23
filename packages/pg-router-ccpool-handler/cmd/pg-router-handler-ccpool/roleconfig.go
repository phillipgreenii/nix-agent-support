package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/rolefile"
	"github.com/phillipgreenii/pg-router-ccpool-handler/internal/roles"
)

// configPath is the path every problem about the role's config is reported
// under; the strict shape check names fields relative to it.
const configPath = "role.config"

// configKeys are the only top-level keys of a ccpool role's [role.config]: the
// ccpool-role fields of the former per-role JSON, names unchanged. name, type,
// survivesShutdown, strictBaseEnv and command are not config (the role's name
// is the request's role.name, survives_shutdown is a core key, and the others
// belong to the command adapter or to no adapter); each is an unknown key
// here. The match is case-insensitive, as encoding/json's is.
var configKeys = map[string]bool{"ccpool": true, "sessionenv": true}

// decodeRole strictly decodes ro.Config into a ccpool roles.Role and returns
// EVERY problem it finds, in a stable order: the strict shape (an unknown key
// or a wrong type anywhere, keys sorted), the sessionEnv key rules (one problem
// per offending key), then the load rules the legacy per-role decode applied
// (actor required, templates parse, durations, effort, prefetch, ...). The role
// is meaningful only when problems is empty.
//
// The per-field shape and load rules are rolefile.RoleFile and
// rolefile.DecodeRole, unchanged, so a role config means exactly what the
// legacy role file meant; this adapter only decides which top-level keys exist.
func decodeRole(ro roleObject) (roles.Role, []string) {
	var problems []string
	known := map[string]any{}
	for _, k := range rolefile.SortedKeys(ro.Config) {
		if !configKeys[strings.ToLower(k)] {
			problems = append(problems, fmt.Sprintf("%s: unknown key %q", configPath, k))
			continue
		}
		known[k] = ro.Config[k]
	}

	shape := rolefile.ShapeChecker{}
	shape.Walk(configPath, known, reflect.TypeOf(rolefile.RoleFile{}), nil)
	problems = append(problems, shape.Problems...)

	// sessionEnv keys: every offending key, not just the first (INV-CCH-33).
	for k, v := range known {
		if !strings.EqualFold(k, "sessionEnv") {
			continue
		}
		env, _ := v.(map[string]any)
		for _, key := range rolefile.SortedKeys(env) {
			if err := rolefile.ValidateSessionEnv(map[string]string{key: ""}); err != nil {
				problems = append(problems, configPath+".sessionEnv: "+err.Error())
			}
		}
	}

	hasCCPool := false
	for k := range known {
		hasCCPool = hasCCPool || strings.EqualFold(k, "ccpool")
	}
	if !hasCCPool {
		problems = append(problems, configPath+".ccpool: required (a role of this adapter launches a ccpool session)")
	}

	// The load rules need a decoded value; a type error means the typed decode
	// would only repeat it (or fail on the first one).
	if !shape.TypeError && hasCCPool {
		legacy := make(map[string]any, len(known)+2)
		for k, v := range known {
			legacy[k] = v
		}
		legacy["name"] = ro.Name
		legacy["type"] = "ccpool"
		b, err := json.Marshal(legacy)
		if err != nil { // unreachable: the config came out of a JSON decode
			return roles.Role{}, append(problems, fmt.Sprintf("%s: %v", configPath, err))
		}
		role, err := rolefile.DecodeRole(configPath, b, false)
		if err != nil {
			// DecodeRole prefixes its message "role config <path>: "; report it under the config path like the other problems.
			problems = append(problems, configPath+": "+strings.TrimPrefix(err.Error(), "role config "+configPath+": "))
		} else if len(problems) == 0 {
			return role, nil
		}
	}
	return roles.Role{}, problems
}
