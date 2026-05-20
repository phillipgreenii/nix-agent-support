// Package cmdplugin helps authors of kubectl-style pg-pr command plugins
// parse argv and environment consistently. Phase 0 stubs only.
package cmdplugin

import "errors"

var ErrNotImplemented = errors.New("cmdplugin: not implemented in this phase")
