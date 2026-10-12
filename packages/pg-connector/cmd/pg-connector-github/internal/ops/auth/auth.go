// Package auth is the daemon's auth_status op for both instances.
//
// The answer comes from the cached identity (internal/identity): the op makes
// no origin call and spends no budget, so a preflight is free however often a
// consumer asks. The result has the shape the old backends answered
// (scriptout.AuthStatus: a state plus an optional detail, never a wire error):
//
//   - authenticated: state OK, no detail;
//   - GitHub rejected the token (the unauthenticated state is set): MISSING, the
//     detail carrying the reason;
//   - the viewer has never been resolved: MISSING, the detail carrying the last
//     error, or saying the identity is not resolved yet;
//   - an identity is cached and only the latest refresh failed for another
//     reason (network, timeout): still OK, the detail saying the refresh is
//     failing, because the credential was never judged bad.
//
// When the daemon is down the client answers unavailable (the client's op
// table), not this package.
package auth

import (
	"context"
	"encoding/json"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/identity"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/router"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// Source is what the op reads: the cached identity and auth state.
// *identity.Tracker satisfies it.
type Source interface {
	Current() identity.Snapshot
}

var _ Source = (*identity.Tracker)(nil)

// Status maps the cached identity onto the auth_status result.
func Status(s identity.Snapshot) scriptout.AuthStatus {
	switch {
	case s.Unauthenticated:
		return scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: "unauthenticated: " + s.AuthError}
	case s.Login == "" && s.LastError != "":
		return scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: s.LastError}
	case s.Login == "":
		return scriptout.AuthStatus{State: scriptout.AuthMissing, Detail: "viewer identity not resolved yet"}
	case s.LastError != "":
		return scriptout.AuthStatus{State: scriptout.AuthOK, Detail: "viewer refresh failing: " + s.LastError}
	default:
		return scriptout.AuthStatus{State: scriptout.AuthOK}
	}
}

// Handler answers auth_status for an instance whose schema version is
// schemaVersion. It never returns an error: the state field carries the
// outcome, as the old backends' handler did.
func Handler(src Source, schemaVersion int) scriptout.OpHandler {
	return scriptout.OpHandler{
		SchemaVersion: schemaVersion,
		Handle: func(context.Context, json.RawMessage) (any, error) {
			return Status(src.Current()), nil
		},
	}
}

// Register installs auth_status under both instances, each stamped with its
// own schema version.
func Register(r *router.Router, src Source) {
	r.Register("pr", scriptout.OpAuthStatus, Handler(src, schema.PRSchemaVersion))
	r.Register("ci", scriptout.OpAuthStatus, Handler(src, schema.CISchemaVersion))
}
