package manager

import (
	"time"

	"github.com/phillipgreenii/pg-gwm/internal/state"
)

// Record is what lease, renew and info return (INV-GWM-4). Absent values are
// null; Metadata is always an object.
type Record struct {
	ID              string            `json:"id"`
	WorktreePath    string            `json:"worktree_path"`
	Repo            string            `json:"repo"`
	Owner           *string           `json:"owner"`
	Branch          *string           `json:"branch"`
	Head            string            `json:"head"`
	Status          state.Status      `json:"status"`
	LeaseID         *string           `json:"lease_id"`
	LeaseTTL        *string           `json:"lease_ttl"`
	LeaseExpiration *string           `json:"lease_expiration"`
	Name            *string           `json:"name"`
	Metadata        map[string]string `json:"metadata"`
	CreatedAt       string            `json:"created_at"`
	Managed         bool              `json:"managed"`
	// Renewed is set only by lease --or-renew.
	Renewed *bool `json:"renewed,omitempty"`
	// Warnings are for standard error; they are not part of the output.
	Warnings []string `json:"-"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// record renders w, whose head commit is head, as a Record.
func (m *Manager) record(w state.Worktree, head string) Record {
	r := Record{
		ID:           w.ID,
		WorktreePath: w.Path,
		Repo:         m.Repo.Label,
		Branch:       strPtr(w.Branch),
		Head:         head,
		Status:       w.Effective(m.now()),
		Metadata:     map[string]string{},
		CreatedAt:    rfc3339(w.CreatedAt),
		Managed:      true,
	}
	if l := w.Lease; l != nil {
		r.Owner = strPtr(l.Owner)
		r.LeaseID = strPtr(l.ID)
		r.Name = strPtr(l.Name)
		if !l.TTL.IsZero() {
			s := l.TTL.String()
			r.LeaseTTL = &s
		}
		if l.Expiry != nil {
			s := rfc3339(*l.Expiry)
			r.LeaseExpiration = &s
		}
		for k, v := range l.Metadata {
			r.Metadata[k] = v
		}
	}
	return r
}
