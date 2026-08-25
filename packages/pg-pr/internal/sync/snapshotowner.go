package sync

import (
	"sort"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/ownership"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/snapshot"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/internal/telemetry"
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

// pruneExpiredMerged removes entries for a merged PR of MINE (own==Mine, the
// only case refresh.go retains past merge) whose snapshot.MergedRetentionWindow
// has elapsed as of now. Without this, such an entry — no longer polled once
// it drops out of the "is:open" fingerprint roster, so refreshPR never revisits
// it — would sit in this map indefinitely even after Build has stopped
// rendering it in the Mine panel; this keeps the retained model bounded to
// match what is actually still visible (pg2-ew4kf). Safe to call only from the
// owner goroutine (same single-mutator contract as upsert/delete).
func (m *snapshotModel) pruneExpiredMerged(now time.Time) {
	for k, v := range m.prs {
		if v.PR.Merged && v.Ownership == ownership.Mine && !snapshot.WithinMergedRetention(v.PR.MergedAt, now) {
			delete(m.prs, k)
		}
	}
}

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
type snapshotUpdate struct {
	Key   prKey
	Input *snapshot.PRInput
}

// runSnapshotOwner owns the model and rebuilds+Sets the store per update until
// updates is closed. Build inputs come from the engine so SIGHUP changes are
// picked up on the next update.
func (e *Engine) runSnapshotOwner(updates <-chan snapshotUpdate, store *snapshot.Store) {
	m := newSnapshotModel()
	for u := range updates {
		if u.Input == nil {
			m.delete(u.Key)
		} else {
			m.upsert(*u.Input)
		}
		now := e.deps.Now()
		m.pruneExpiredMerged(now)
		store.Set(snapshot.Build(snapshot.BuilderInput{
			GeneratedAt:             now,
			SyncIntervalSeconds:     int(e.deps.SyncInterval.Seconds()),
			Self:                    e.cfg().SelfLogin,
			TeamMembers:             e.allTeamMembers(),
			WatchLabels:             e.allWatchLabels(),
			Registry:                e.deps.AgentRegistry,
			PRs:                     m.sortedInputs(),
			CheckInterpretersByRepo: e.checkInterpretersByRepo(),
			// See sync.go's buildAndStoreSnapshot for why this daemon-owned
			// shared snapshot always includes hidden PRs (pg2-4dz88.4.3).
			IncludeHidden: true,
		}))
		// A snapshot has been published for the dashboard. The retired
		// full-Sync path set this; the daemon's per-PR owner must too, or the
		// Ops "snapshot present" tile reads absent for the daemon's lifetime.
		telemetry.SnapshotPresent.Set(1)
	}
}
