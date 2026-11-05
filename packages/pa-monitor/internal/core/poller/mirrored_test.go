package poller

import (
	"testing"

	"github.com/phillipgreenii/pa-monitor/internal/core/corpus/corpustest"
)

// mirrored feeds a recorder from fixture directories written the old way (a
// sessions directory and a Claude home) and points p at it: the Poller then
// reads sessions and files through the recorder like the daemon does.
func mirrored(t *testing.T, sessionsDir, claudeHome string, alive func(int) bool, p *Poller) *Poller {
	t.Helper()
	p, _ = mirroredWith(t, sessionsDir, claudeHome, alive, p)
	return p
}

// mirroredWith is mirrored and also returns the Mirror, for a test that changes
// its fixtures afterwards and calls Sync.
func mirroredWith(t *testing.T, sessionsDir, claudeHome string, alive func(int) bool, p *Poller) (*Poller, *corpustest.Mirror) {
	t.Helper()
	m := corpustest.StartMirror(t, sessionsDir, claudeHome, alive)
	p.Registry, p.Facts = m.Facts, m.Facts
	return p, m
}
