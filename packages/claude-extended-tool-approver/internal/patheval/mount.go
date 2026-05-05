package patheval

import (
	"path/filepath"
	"strings"
)

// Mount describes a host→container bind mount active during evaluation of a
// docker inner command.
type Mount struct {
	HostPath      string // absolute, symlink-resolved host path
	ContainerPath string // absolute container path
	ReadOnly      bool   // true for :ro / readonly mounts
}

// WithMounts returns a copy of pe operating in container mode. A non-nil
// slice (even empty) enables container semantics: paths not covered by any
// mount are treated as container-internal and approved.
func (pe *PathEvaluator) WithMounts(mounts []Mount) *PathEvaluator {
	normalized := make([]Mount, 0, len(mounts))
	for _, m := range mounts {
		normalized = append(normalized, Mount{
			HostPath:      filepath.Clean(m.HostPath),
			ContainerPath: filepath.Clean(m.ContainerPath),
			ReadOnly:      m.ReadOnly,
		})
	}
	clone := *pe
	clone.mounts = normalized
	clone.inContainer = true
	return &clone
}

// evaluateContainer handles Evaluate when the evaluator is in container mode.
// Called from Evaluate() after cleanPath.
func (pe *PathEvaluator) evaluateContainer(cleaned string) PathAccess {
	// Find longest-prefix mount match.
	var best *Mount
	bestLen := -1
	for i := range pe.mounts {
		m := &pe.mounts[i]
		if m.ContainerPath == "" || m.HostPath == "" {
			continue
		}
		if cleaned == m.ContainerPath || strings.HasPrefix(cleaned+"/", m.ContainerPath+"/") {
			if len(m.ContainerPath) > bestLen {
				best = m
				bestLen = len(m.ContainerPath)
			}
		}
	}
	if best == nil {
		// No mount covers this path — it's container-internal and ephemeral.
		return PathReadWrite
	}
	// Translate container path → host path, then evaluate with container mode
	// disabled (so the host evaluator classifies it normally).
	suffix := strings.TrimPrefix(cleaned, best.ContainerPath)
	hostPath := filepath.Clean(best.HostPath + suffix)
	hostEval := *pe
	hostEval.inContainer = false
	hostEval.mounts = nil
	access := hostEval.Evaluate(hostPath)
	if access == PathReject {
		return PathReject
	}
	if best.ReadOnly && access == PathReadWrite {
		return PathReadOnly
	}
	return access
}
