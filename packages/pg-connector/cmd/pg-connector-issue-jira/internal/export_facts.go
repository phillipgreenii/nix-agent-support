// export_facts.go: the exported form of the operator-facts logic, for the
// daemon's detail fetcher. Show's enrichOperatorFacts makes one extra
// `pjira search --expand changelog,comments` call for an issue assigned to the
// operator; the detail fetcher already holds the issue WITH its changelog and
// comments (it fetched it that way), so OperatorFacts computes the same two
// facts from the issue as fetched and makes no search of its own.
package internal

import (
	"context"
	"sync"
)

// operatorCache holds the operator identity once it has been resolved, so the
// daemon resolves it once and not on every detail fetch.
type operatorCache struct {
	mu sync.Mutex
	me *operatorIdentity
}

// cachedOperator returns the operator identity, resolving it on the first
// call that succeeds and answering from the cache afterwards. A failed
// resolution is not cached, so the next call tries again. A Backend with no
// cache resolves on every call. The identity returned is the caller's own
// copy: learning an account id from one issue never changes another call's.
func (b *Backend) cachedOperator(ctx context.Context) (*operatorIdentity, error) {
	c := b.opCache
	if c == nil {
		return b.resolveOperator(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.me == nil {
		me, err := b.resolveOperator(ctx)
		if err != nil {
			return nil, err
		}
		c.me = me
	}
	return c.me.clone(), nil
}

// clone returns an independent copy of the identity.
func (o *operatorIdentity) clone() *operatorIdentity {
	cp := newOperatorIdentity()
	for e := range o.emails {
		cp.emails[e] = true
	}
	for id := range o.accountIDs {
		cp.accountIDs[id] = true
	}
	return cp
}

// OperatorFacts is the exported form of the logic enrichOperatorFacts runs: for
// an issue assigned to the operator it returns when the issue entered its
// current status and when the operator last updated it, both RFC 3339 UTC and
// "" when they cannot be dated; for every other issue (unassigned, assigned to
// someone else, a key outside the Jira key shape, or an operator identity that
// cannot be established) both are empty.
//
// iss MUST be the issue as fetched with its changelog and comments (a
// `pjira search --expand changelog,comments` item): no second search is made.
// The operator identity is resolved once and cached on the Backend. The
// Backend the daemon calls this on is built with WithGetenv over its configured
// pjira_env, so the identity never comes from the process environment.
func (b *Backend) OperatorFacts(ctx context.Context, iss *PjiraIssue) (statusChangedAt, operatorUpdatedAt string) {
	if iss == nil || iss.Assignee == nil || !issueKeyRE.MatchString(iss.Key) {
		return "", ""
	}
	me, err := b.cachedOperator(ctx)
	if err != nil {
		return "", ""
	}
	me.learn(*iss.Assignee)
	if !me.Matches(*iss.Assignee) {
		return "", ""
	}
	return operatorFacts(me, iss, iss.Status)
}
