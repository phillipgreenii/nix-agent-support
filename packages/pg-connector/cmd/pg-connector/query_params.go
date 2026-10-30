// query_params.go: the --param flag of the list verbs and its capability
// check (bead pg2-eclw3j.3).
//
// A parameterized named query takes VALUES from the request; its name,
// expressions and parameter patterns come only from the backend's own config
// (INV-ERR-3, INV-STATE-1). A backend that predates the feature decodes list
// args leniently and would ignore "params", so the umbrella asks capabilities
// first and refuses unless the backend declares vocabulary.query_params. An
// umbrella that predates the feature has no --param flag at all, so its caller
// fails closed.
package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// vocabularyQueryParamsKey is the capabilities.vocabulary key a backend sets
// to declare that it substitutes list params into its configured queries.
const vocabularyQueryParamsKey = "query_params"

// parseParamFlags turns repeated --param name=value flags into a map. A flag
// without "=", with an empty name, or naming the same param twice is
// invalid_argument. An empty value is kept (the declared pattern decides).
func parseParamFlags(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, kv := range raw {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("--param %q must be name=value", kv))
		}
		if _, dup := out[name]; dup {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, fmt.Sprintf("--param %q given more than once", name))
		}
		out[name] = value
	}
	return out, nil
}

// backendDeclaresQueryParams asks backend's capabilities whether its
// vocabulary carries query_params as the bool true. Any other value, or an
// absent key, is "not declared".
func backendDeclaresQueryParams(ctx context.Context, reg *Registry, backend string) (bool, error) {
	resp, err := reg.InvokeCapabilities(ctx, backend)
	if err != nil {
		return false, err
	}
	b, _ := resp.Vocabulary[vocabularyQueryParamsKey].(bool)
	return b, nil
}

// requireQueryParamsCapability refuses (invalid_argument) when any of backends
// does not declare vocabulary.query_params, naming them; a backend whose
// capabilities cannot be read fails the call as unavailable (fail closed).
func requireQueryParamsCapability(ctx context.Context, reg *Registry, backends []string) error {
	var missing []string
	for _, b := range backends {
		ok, err := backendDeclaresQueryParams(ctx, reg, b)
		if err != nil {
			return scriptout.WrapError(scriptout.ErrUnavailable, fmt.Sprintf("--param: cannot read capabilities of %q: %v", b, err))
		}
		if !ok {
			missing = append(missing, b)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return scriptout.WrapError(scriptout.ErrInvalidArgument,
			fmt.Sprintf("--param is not supported by backend(s) %s: their capabilities do not declare vocabulary.%s (backend binary and config must ship together)", strings.Join(missing, ", "), vocabularyQueryParamsKey))
	}
	return nil
}
