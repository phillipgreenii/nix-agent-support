// history.go: the list_history op (design 3.2, 4, 9.2, bead pg2-rwuhs).
//
// Grafana has no bulk history endpoint (without ruleUID it answers HTTP 500
// "ruleUID is required to query annotations"): the backend enumerates rules
// (client.go Rules) and asks for each rule's state-history frame once
// (RuleHistory), with bounded concurrency, a per-request timeout and an
// op-wide budget; rules that fail or run out of budget make the result
// partial (Truncated) instead of failing the op, unless no rule answered. A frame is column-oriented,
// `{data: {values: [times[], texts[], prevs[], nexts[], datas[]]}}`, one
// column slot per state transition; times are MICROSECONDS since the epoch.
// A row's firing-instance label set is not structured data: it is embedded
// in texts[i], shaped `<rule title> {k=v, k2=v2} - <data string>`. That
// parsing is source-specific fragility and lives here, in the backend, and
// only here (design 9.2).
//
// Episodes: a transition INTO "Alerting" opens an episode for its label set;
// the next transition OUT of "Alerting" for the same label set closes it. An
// episode with no observed close inside the requested window has no
// ended_at. Rows that cannot be paired (a close with no open, e.g. the window
// began mid-episode) are ignored.
//
// Degradation: a text field that does not parse yields an episode with only
// the synthesized rule-level attributes (never a panic, never dropped); a
// frame with a structurally wrong shape is a malformed response and answers
// unavailable (design 8.1). Attention never reaches this file.
package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// historyLimit is the per-rule row cap sent to Grafana (lat-survey's value).
// A rule returning exactly this many rows may have more, so the result is
// marked truncated.
const historyLimit = 1000

// historyConcurrency bounds in-flight per-rule history requests. It is a
// variable so tests can pin it.
var historyConcurrency = 8

// historyRuleTimeout is the budget of one per-rule request; a rule that
// exceeds it is skipped and the result is marked truncated.
var historyRuleTimeout = 10 * time.Second

// historyBudgetMargin is how long before the op deadline the backend stops
// starting and awaiting requests, leaving time to answer with what it has.
var historyBudgetMargin = 5 * time.Second

// alertingState is the state-history state name an episode starts in.
const alertingState = "Alerting"

// ListHistory implements alert.Provider.ListHistory.
func (b *Backend) ListHistory(ctx context.Context, since, until time.Time, query schema.QueryExpr) (*schema.AlertHistoryResult, error) {
	cfg, err := decodeConfig(ctx)
	if err != nil {
		return nil, err
	}
	if until.Before(since) {
		return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "alert-grafana: until is before since")
	}
	sets := make([][]labelMatcher, 0, len(query))
	for _, el := range query {
		set, err := parseMatcherSet(el)
		if err != nil {
			return nil, scriptout.WrapError(scriptout.ErrInvalidArgument, "alert-grafana: "+err.Error())
		}
		sets = append(sets, set)
	}

	rules, err := b.transport.Rules(ctx, cfg.BaseURL)
	if err != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "alert-grafana: "+err.Error())
	}

	// Distinct, non-empty rule uids in ruler order.
	seen := make(map[string]bool, len(rules))
	uniq := make([]apiRule, 0, len(rules))
	for _, rule := range rules {
		if rule.UID == "" || seen[rule.UID] {
			continue
		}
		seen[rule.UID] = true
		uniq = append(uniq, rule)
	}

	outcomes := b.fetchHistories(ctx, cfg.BaseURL, uniq, since, until)

	res := &schema.AlertHistoryResult{Episodes: []schema.AlertEpisode{}}
	var firstErr error
	succeeded := 0
	for i, o := range outcomes {
		if o.malformed != nil {
			return nil, o.malformed
		}
		if !o.done {
			// Failed, timed out or never started: the result is partial.
			res.Truncated = true
			if firstErr == nil {
				firstErr = o.err
			}
			continue
		}
		succeeded++
		if len(o.rows) >= historyLimit {
			res.Truncated = true
		}
		for _, ep := range episodesFromRows(uniq[i], o.rows) {
			if matchesAnySet(sets, ep) {
				res.Episodes = append(res.Episodes, ep)
			}
		}
	}
	if succeeded == 0 && firstErr != nil {
		return nil, scriptout.WrapError(scriptout.ErrUnavailable, "alert-grafana: "+firstErr.Error())
	}

	sort.SliceStable(res.Episodes, func(i, j int) bool {
		return res.Episodes[i].StartedAt < res.Episodes[j].StartedAt
	})
	return res, nil
}

// historyOutcome is one rule's fetch result. done is false when the request
// failed, hit its budget or was never started; malformed is a structurally
// wrong frame, which fails the whole op (a partial result cannot hide it).
type historyOutcome struct {
	done      bool
	rows      []historyRow
	err       error
	malformed error
}

// fetchHistories reads every rule's history with bounded concurrency. Each
// request gets historyRuleTimeout, and no new request starts once the
// op-wide budget (the context deadline less historyBudgetMargin) is spent, so
// a loaded Grafana yields a partial result instead of a timeout that loses
// everything. The returned slice is index-aligned with rules.
func (b *Backend) fetchHistories(ctx context.Context, baseURL string, rules []apiRule, since, until time.Time) []historyOutcome {
	out := make([]historyOutcome, len(rules))
	budget := ctx
	if dl, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		budget, cancel = context.WithDeadline(ctx, dl.Add(-historyBudgetMargin))
		defer cancel()
	}
	idx := make(chan int)
	var wg sync.WaitGroup
	workers := min(historyConcurrency, max(len(rules), 1))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				if budget.Err() != nil {
					out[i].err = budget.Err()
					continue
				}
				rctx, cancel := context.WithTimeout(budget, historyRuleTimeout)
				frame, err := b.transport.RuleHistory(rctx, baseURL, rules[i].UID, since, until, historyLimit)
				cancel()
				if err != nil {
					out[i].err = err
					continue
				}
				rows, err := decodeHistoryRows(frame)
				if err != nil {
					out[i].malformed = scriptout.WrapError(scriptout.ErrUnavailable,
						fmt.Sprintf("alert-grafana: malformed history for rule %s: %v", rules[i].UID, err))
					continue
				}
				out[i].rows, out[i].done = rows, true
			}
		}()
	}
	for i := range rules {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return out
}

// matchesAnySet reports whether the episode satisfies the query: no sets
// means everything; otherwise the sets are ORed and each set's matchers ANDed
// over the episode's label view (attributes without the "label." prefix).
func matchesAnySet(sets [][]labelMatcher, ep schema.AlertEpisode) bool {
	if len(sets) == 0 {
		return true
	}
	labels := make(map[string]string, len(ep.Attributes))
	for k, v := range ep.Attributes {
		if name, ok := strings.CutPrefix(k, "label."); ok {
			labels[name] = v
		}
	}
	for _, set := range sets {
		if matchesAll(set, labels) {
			return true
		}
	}
	return false
}

// historyRow is one decoded state transition.
type historyRow struct {
	at   time.Time
	text string
	prev string
	next string
}

// decodeHistoryRows validates the frame's shape and decodes its columns into
// rows sorted by time. A missing data object, fewer than four columns
// (unless the frame has no columns at all, which Grafana may use for "no
// history"), non-numeric times, or ragged columns are errors. A null or
// non-string text cell is NOT an error: it decodes to "" so the episode
// degrades instead of the whole call failing.
func decodeHistoryRows(h apiHistory) ([]historyRow, error) {
	if h.Data == nil {
		return nil, fmt.Errorf("response has no data object")
	}
	cols := h.Data.Values
	if len(cols) == 0 {
		return nil, nil
	}
	if len(cols) < 4 {
		return nil, fmt.Errorf("frame has %d columns, want at least 4", len(cols))
	}
	var times []float64
	if err := json.Unmarshal(cols[0], &times); err != nil {
		return nil, fmt.Errorf("times column: %w", err)
	}
	var texts []json.RawMessage
	if err := json.Unmarshal(cols[1], &texts); err != nil {
		return nil, fmt.Errorf("texts column: %w", err)
	}
	var prevs, nexts []string
	if err := json.Unmarshal(cols[2], &prevs); err != nil {
		return nil, fmt.Errorf("prev-state column: %w", err)
	}
	if err := json.Unmarshal(cols[3], &nexts); err != nil {
		return nil, fmt.Errorf("next-state column: %w", err)
	}
	n := len(times)
	if len(texts) != n || len(prevs) != n || len(nexts) != n {
		return nil, fmt.Errorf("ragged columns: %d times, %d texts, %d prev, %d next", n, len(texts), len(prevs), len(nexts))
	}

	rows := make([]historyRow, n)
	for i := range rows {
		var text string
		if err := json.Unmarshal(texts[i], &text); err != nil {
			text = "" // null or non-string: degrade, do not fail
		}
		rows[i] = historyRow{
			at:   time.UnixMicro(int64(times[i])).UTC(),
			text: text,
			prev: prevs[i],
			next: nexts[i],
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at.Before(rows[j].at) })
	return rows, nil
}

// episodesFromRows pairs Alerting transitions into episodes for one rule.
func episodesFromRows(rule apiRule, rows []historyRow) []schema.AlertEpisode {
	var episodes []schema.AlertEpisode
	open := make(map[string]int) // label-set key -> index into episodes
	for _, r := range rows {
		labels, parsed := parseHistoryLabels(r.text)
		key := labelSetKey(labels)
		if !parsed {
			key = "\x00text:" + r.text
		}
		switch {
		case r.next == alertingState:
			if _, isOpen := open[key]; isOpen {
				continue // already alerting for this label set
			}
			open[key] = len(episodes)
			episodes = append(episodes, newEpisode(rule, labels, r.at))
		case r.prev == alertingState:
			if i, isOpen := open[key]; isOpen {
				episodes[i].EndedAt = r.at.Format(time.RFC3339)
				delete(open, key)
			}
		}
	}
	return episodes
}

// newEpisode builds one episode. Attributes are the parsed labels under the
// "label." namespace (the same namespace Alert.attributes uses), with the
// rule-level labels alertname and __alert_rule_uid__ synthesized from the
// ruler when the history text omits them, so an episode and a firing alert
// for the same rule are comparable on the same keys.
func newEpisode(rule apiRule, labels map[string]string, at time.Time) schema.AlertEpisode {
	attrs := make(map[string]string, len(labels)+2)
	for k, v := range labels {
		attrs["label."+k] = v
	}
	if _, ok := labels["alertname"]; !ok && rule.Title != "" {
		attrs["label.alertname"] = rule.Title
	}
	if _, ok := labels[ruleUIDLabel]; !ok {
		attrs["label."+ruleUIDLabel] = rule.UID
	}
	title := rule.Title
	if title == "" {
		title = labels["alertname"]
	}
	if title == "" {
		title = rule.UID
	}
	return schema.AlertEpisode{
		RuleID:     rule.UID,
		Title:      title,
		StartedAt:  at.Format(time.RFC3339),
		Severity:   severityFromLabel(labels["severity"]),
		Attributes: attrs,
	}
}

// parseHistoryLabels extracts the `{k=v, k2=v2}` block from a history text
// field. Pairs are split on ", " and each on its FIRST "=" (matching
// lat-survey's documented parse); a pair with no "=" or an empty key is
// skipped. parsed is false when the text has no brace block at all. It never
// panics on any input.
func parseHistoryLabels(text string) (labels map[string]string, parsed bool) {
	labels = map[string]string{}
	open := strings.Index(text, "{")
	if open < 0 {
		return labels, false
	}
	rest := text[open+1:]
	end := strings.Index(rest, "}")
	if end < 0 {
		return labels, false
	}
	for _, pair := range strings.Split(rest[:end], ", ") {
		k, v, ok := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		labels[k] = v
	}
	return labels, true
}

// labelSetKey is a canonical, order-independent key for a label set.
func labelSetKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte('\n')
	}
	return b.String()
}
