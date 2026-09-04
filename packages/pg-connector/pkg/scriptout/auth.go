package scriptout

// AuthState enumerates the standard auth states a backend may return from
// the auth_status op. Carried over unchanged from pg-pr's existing
// convention.
type AuthState string

const (
	AuthOK                 AuthState = "OK"
	AuthMissing            AuthState = "MISSING"
	AuthExpired            AuthState = "EXPIRED"
	AuthInsufficientScopes AuthState = "INSUFFICIENT_SCOPES"
)

// AuthStatus is the result payload of the auth_status op — carried over
// unchanged from pg-pr's existing convention. It travels inside a normal
// Response's Result field, wrapped with the usual protocolVersion +
// schemaVersion envelope like any other op.
type AuthStatus struct {
	State  AuthState `json:"state"`
	Detail string    `json:"detail,omitempty"`
}
