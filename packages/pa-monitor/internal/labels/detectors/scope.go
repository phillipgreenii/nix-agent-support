package detectors

import "github.com/phillipgreenii/pa-monitor/internal/labels"

// DefaultScope sets workspace.scope to a sentinel value so every session
// has a non-empty workspace.scope label. Higher-priority label producers
// (e.g. the pa-monitor-decorator-scope shell-out decorator) override this
// via labels.Set.Merge — that's the whole point of putting this first in
// the detector list. Without it, the Grafana "Sessions by workspace.scope"
// panel only has data when at least one downstream-scoped session is
// running.
type DefaultScope struct{}

func (DefaultScope) Name() string { return "default_scope" }

func (DefaultScope) Detect(s labels.Session) labels.Set {
	return labels.Set{"workspace.scope": "personal"}
}
