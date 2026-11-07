package main

import (
	"testing"

	"github.com/phillipgreenii/pg-router-handlers/internal/ccpool"
)

// A pooled slot path is reused by successive beads. A live session of an
// earlier lease at the same path is not a peer of the current lease's session,
// so it neither pins the slot nor blocks its release; a non-pooled session at
// the same path still is a peer.
func TestPeerGuards_PooledPathAttributedByLease(t *testing.T) {
	const slot = "/pool/slot-1"
	row := func(id, lease string, st ccpool.SessionState) ccpool.Session {
		s := ccpool.Session{ExternalID: id, CWD: slot, Live: true, State: st}
		if lease != "" {
			s.Meta = map[string]string{ccpool.MetaKeyCheckoutLease: lease}
		}
		return s
	}
	self := row("self", "lease-b", ccpool.StateIdle)

	prior := row("prior", "lease-a", ccpool.StateWorking)
	if worktreeHeldByLivePeer([]ccpool.Session{prior}, self) {
		t.Error("worktreeHeldByLivePeer: a session of another lease must not be a peer")
	}
	if worktreeInUseByPeer([]ccpool.Session{prior}, self) {
		t.Error("worktreeInUseByPeer: a session of another lease must not be a peer")
	}
	if worktreeHeldBySparedPeer([]ccpool.Session{prior}, self) {
		t.Error("worktreeHeldBySparedPeer: a session of another lease must not be a peer")
	}

	same := row("same", "lease-b", ccpool.StateWorking)
	if !worktreeHeldByLivePeer([]ccpool.Session{same}, self) || !worktreeInUseByPeer([]ccpool.Session{same}, self) {
		t.Error("a second session of the SAME lease must remain a peer")
	}

	plainSelf := row("plain-self", "", ccpool.StateIdle)
	plainPeer := row("plain-peer", "", ccpool.StateWorking)
	if !worktreeHeldByLivePeer([]ccpool.Session{plainPeer}, plainSelf) || !worktreeInUseByPeer([]ccpool.Session{plainPeer}, plainSelf) {
		t.Error("non-pooled sessions at one path must stay path-keyed peers")
	}
}
