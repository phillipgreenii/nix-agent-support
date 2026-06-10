package sync

import (
	"sort"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
)

// snapshotModel holds the authoritative per-PR inputs. Not concurrency-safe by
// itself; the owner goroutine is its sole mutator.
type snapshotModel struct {
	prs map[prKey]snapshot.PRInput
}

func newSnapshotModel() *snapshotModel {
	return &snapshotModel{prs: map[prKey]snapshot.PRInput{}}
}

func (m *snapshotModel) upsert(in snapshot.PRInput) {
	m.prs[prKey{Repo: in.PR.Repo, Number: in.PR.Number}] = in
}
func (m *snapshotModel) delete(k prKey) { delete(m.prs, k) }

// sortedInputs returns inputs deterministically ordered by repo then number,
// so per-PR rebuilds don't reshuffle dashboard rows.
func (m *snapshotModel) sortedInputs() []snapshot.PRInput {
	out := make([]snapshot.PRInput, 0, len(m.prs))
	for _, v := range m.prs {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PR.Repo != out[j].PR.Repo {
			return out[i].PR.Repo < out[j].PR.Repo
		}
		return out[i].PR.Number < out[j].PR.Number
	})
	return out
}

// snapshotUpdate is the message workers send the owner. Input==nil → delete.
//
//nolint:unused // consumed by the daemon owner-goroutine wiring landed in a follow-up task.
type snapshotUpdate struct {
	Key   prKey
	Input *snapshot.PRInput
}

// runSnapshotOwner owns the model and rebuilds+Sets the store per update until
// updates is closed. Build inputs come from the engine so SIGHUP changes are
// picked up on the next update.
//
//nolint:unused // invoked by the daemon owner-goroutine wiring landed in a follow-up task.
func (e *Engine) runSnapshotOwner(updates <-chan snapshotUpdate, store *snapshot.Store) {
	m := newSnapshotModel()
	for u := range updates {
		if u.Input == nil {
			m.delete(u.Key)
		} else {
			m.upsert(*u.Input)
		}
		store.Set(snapshot.Build(snapshot.BuilderInput{
			GeneratedAt:         e.deps.Now(),
			SyncIntervalSeconds: int(e.deps.SyncInterval.Seconds()),
			Self:                e.deps.Cfg.SelfLogin, // Task 7 migrates to e.cfg().SelfLogin
			TeamMembers:         e.allTeamMembers(),
			Registry:            e.deps.AgentRegistry,
			PRs:                 m.sortedInputs(),
		}))
	}
}
