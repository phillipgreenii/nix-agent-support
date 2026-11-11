package status

import (
	"encoding/json"
	"fmt"
	"strings"
)

// JSON renders the report as one line.
func (r *Report) JSON() ([]byte, error) { return json.Marshal(r) }

// Human renders the report for a person, without a trailing newline.
func (r *Report) Human() string {
	var b strings.Builder
	fmt.Fprintf(&b, "repo %s", r.Repo)
	if r.Branch != "" {
		fmt.Fprintf(&b, " (branch %s)", r.Branch)
	}
	b.WriteString("\n\nlocks:")
	if len(r.Locks) == 0 {
		b.WriteString(" none")
	}
	for _, l := range r.Locks {
		if l.Unreadable {
			fmt.Fprintf(&b, "\n  %s  UNREADABLE: %s (a person removes the ref)", l.Ref, l.Error)
			continue
		}
		fmt.Fprintf(&b, "\n  %s  %s %s %s %s", l.Ref, l.Role, l.State, l.Operation, l.Branch)
		fmt.Fprintf(&b, "\n    op_id %s  stage %s  age %ds", l.OpID, l.Stage, l.AgeS)
		if l.Owner != nil {
			fmt.Fprintf(&b, "  owner pid %d on %s", l.Owner.PID, l.Owner.Host)
		}
		if l.Freshness != "" {
			fmt.Fprintf(&b, "  lease %s", l.Freshness)
		}
		if c := l.Command; c != nil {
			fmt.Fprintf(&b, "\n    running %s (pid %d, deadline %s)", c.Argv0, c.PID, c.Deadline)
		}
	}
	b.WriteString("\n\nstopped operations:")
	if len(r.Stopped) == 0 {
		b.WriteString(" none")
	}
	for _, s := range r.Stopped {
		fmt.Fprintf(&b, "\n  %s %s: %s (stage %s, since %s)", s.Operation, s.Branch, s.Reason, s.Stage, s.Since)
		if s.Worktree != "" {
			fmt.Fprintf(&b, "\n    work worktree: %s", s.Worktree)
		}
		if s.Log != "" {
			fmt.Fprintf(&b, "\n    log: pg-git-lander log %s", s.Log)
		}
		for _, n := range s.Next {
			fmt.Fprintf(&b, "\n    next: %s", n)
		}
	}
	b.WriteString("\n\norphan work branches:")
	if len(r.Orphans) == 0 {
		b.WriteString(" none")
	}
	for _, o := range r.Orphans {
		fmt.Fprintf(&b, "\n  %s", o.Branch)
		for _, w := range o.Worktrees {
			fmt.Fprintf(&b, "\n    worktree: %s", w)
		}
	}
	b.WriteString("\n\nstale ref .lock files:")
	if len(r.StaleLock) == 0 {
		b.WriteString(" none")
	}
	for _, s := range r.StaleLock {
		fmt.Fprintf(&b, "\n  %s  (%s, %ds old)", s.Path, s.Namespace, s.AgeS)
	}
	b.WriteString("\n\nconfiguration:")
	for _, s := range r.Config.Sources {
		present := "absent"
		if s.Present {
			present = "present"
		}
		fmt.Fprintf(&b, "\n  %s %s (%s)", s.Kind, s.Path, present)
	}
	fmt.Fprintf(&b, "\n  primary %s, lease_ttl %ds, wait %ds", r.Config.Primary, r.Config.LeaseTTLS, r.Config.WaitS)
	fmt.Fprintf(&b, "\n  trust_repo_config %t, ignore_repo_config %t", r.Config.TrustRepoConfig, r.Config.IgnoreRepoConfig)
	if r.Config.Error != "" {
		fmt.Fprintf(&b, "\n  configuration error: %s", r.Config.Error)
	}
	return b.String()
}
