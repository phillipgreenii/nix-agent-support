package cli

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/wire"
)

var (
	clockOnly = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
	ulidLike  = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
)

// parseAt reads the value of --at: a time today in the host zone (`9:05`), a
// time yesterday (`yesterday 21:30`), or an RFC 3339 instant. It returns the
// instant as RFC 3339 in UTC, which the daemon validates like effective_at.
func (s *state) parseAt(v string) (string, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC().Format(time.RFC3339Nano), nil
	}
	days := 0
	rest := v
	if after, ok := strings.CutPrefix(strings.ToLower(v), "yesterday"); ok {
		days, rest = -1, strings.TrimSpace(after)
	}
	m := clockOnly.FindStringSubmatch(rest)
	if m == nil {
		return "", usagef("--at %q is not a time today (such as 9:05), `yesterday HH:MM`, or an RFC 3339 instant", v)
	}
	z, err := s.hostZone()
	if err != nil {
		return "", err
	}
	var h, min int
	_, _ = fmt.Sscanf(m[1], "%d", &h)
	_, _ = fmt.Sscanf(m[2], "%d", &min)
	if h > 23 || min > 59 {
		return "", usagef("--at %q is not a valid time of day", v)
	}
	now := s.app.Now().In(z.Location())
	t := time.Date(now.Year(), now.Month(), now.Day()+days, h, min, 0, 0, z.Location())
	return t.UTC().Format(time.RFC3339Nano), nil
}

// withAt adds effective_at to a body when --at was given.
func (s *state) withAt(body map[string]any, at string) error {
	if at == "" {
		return nil
	}
	v, err := s.parseAt(at)
	if err != nil {
		return err
	}
	body["effective_at"] = v
	return nil
}

// taskID resolves a task name against the current periods: a full id, a
// definition name, or a unique prefix of a definition name.
func (s *state) taskID(ctx context.Context, name string) (string, error) {
	_, st, err := s.client().State(ctx)
	if err != nil {
		return "", err
	}
	var prefix []wire.Task
	for _, t := range st.Tasks {
		if t.ID == name || t.Definition == name {
			return t.ID, nil
		}
		if strings.HasPrefix(t.Definition, name) {
			prefix = append(prefix, t)
		}
	}
	switch len(prefix) {
	case 1:
		return prefix[0].ID, nil
	case 0:
		return "", usagef("no task of the current periods is called %q", name)
	}
	names := make([]string, len(prefix))
	for i, t := range prefix {
		names[i] = t.Definition
	}
	return "", usagef("%q matches several tasks (%s); name one", name, strings.Join(names, ", "))
}

// cycleRef resolves a cycle named on the command line: an id, or a unique
// prefix of the title of a cycle that is not stopped. An empty name stays empty
// and the daemon decides, answering cycle_ambiguous when it cannot. A stopped
// cycle is named by its id.
func (s *state) cycleRef(ctx context.Context, name string) (string, error) {
	if name == "" || ulidLike.MatchString(name) {
		return name, nil
	}
	_, st, err := s.client().State(ctx)
	if err != nil {
		return "", err
	}
	var cands []wire.Cycle
	if st.Focus != nil {
		cands = append(cands, *st.Focus)
	}
	for _, d := range st.Dimmed {
		cands = append(cands, d.Cycle)
	}
	lower := strings.ToLower(name)
	var hits []wire.Cycle
	for _, c := range cands {
		if c.ID == name {
			return c.ID, nil
		}
		if strings.HasPrefix(strings.ToLower(c.Title), lower) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0].ID, nil
	case 0:
		return "", usagef("no cycle that is not stopped has an id or a title starting with %q", name)
	}
	var lines []string
	for _, c := range hits {
		lines = append(lines, fmt.Sprintf("%s (%s)", c.Title, c.ID))
	}
	return "", usagef("%q matches several cycles: %s; name one by its id", name, strings.Join(lines, ", "))
}
