// alertcontext.go: the investigation context a grafanaAlert finding's bead
// body carries so a triager does not have to go back to Grafana, Loki and
// the bead tracker for it: the rule's dashboard link, its annotations
// verbatim, the last log lines, the current query value and the open beads
// already filed for the same rule.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// metaRuleUID records the Grafana rule uid on a bead so a later bead for
	// the same rule can list it as related.
	metaRuleUID = "pg_router_escalation_rule_uid"
	// logQueryAnnotation names the rule annotation holding the LogQL query
	// that produces the alert's matching log lines. A rule without it gets
	// no log section content (the body says so).
	logQueryAnnotation = "logql"
	// recentLogLines is how many log lines the body carries.
	recentLogLines = 20
	// logLookback is how far back the log query reaches.
	logLookback = time.Hour
)

// alertContext is the per-finding enrichment rendered into the body.
type alertContext struct {
	// LogQuery is the query behind Logs ("" when the rule names none).
	LogQuery string
	// Logs are the newest matching lines; LogsNote explains an empty Logs
	// (not configured, query failed, no matches).
	Logs     []string
	LogsNote string
	// Related are the open beads filed for the same rule uid.
	Related []string
}

// relatedBeads returns the sorted ids of the non-closed beads that carry the
// finding's rule uid (metaRuleUID), excluding any bead whose fingerprint is
// the finding's own bead identity.
func relatedBeads(f finding, existing []connectorIssue) []string {
	if f.RuleUID == "" {
		return nil
	}
	own := bodyFingerprint(f)
	var ids []string
	for _, e := range existing {
		if e.Metadata[metaRuleUID] != f.RuleUID || e.Metadata[metaFingerprint] == own {
			continue
		}
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}

// buildAlertContext gathers the context for f. fetchLogs may be nil (log
// fetching not wired); a failed fetch is recorded in the note, never an
// error: context is best effort and must not block filing the bead.
func buildAlertContext(ctx context.Context, f finding, existing []connectorIssue,
	fetchLogs func(ctx context.Context, query string) ([]string, error),
) alertContext {
	ac := alertContext{Related: relatedBeads(f, existing), LogQuery: f.Annotations[logQueryAnnotation]}
	switch {
	case ac.LogQuery == "":
		ac.LogsNote = "no \"" + logQueryAnnotation + "\" annotation on the rule"
	case fetchLogs == nil:
		ac.LogsNote = "log fetching not configured (--loki-url unset); run the query above"
	default:
		lines, err := fetchLogs(ctx, ac.LogQuery)
		switch {
		case err != nil:
			ac.LogsNote = "log fetch failed: " + err.Error()
		case len(lines) == 0:
			ac.LogsNote = "query returned no lines"
		default:
			ac.Logs = lines
		}
	}
	return ac
}

// renderAlertContext renders the body's "Context:" section for a grafana
// alert finding ("" for any other kind).
func renderAlertContext(f finding) string {
	if f.Kind != kindGrafanaAlert {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nContext:\n")
	if f.DashboardURL != "" {
		b.WriteString("Dashboard: " + f.DashboardURL + "\n")
	}
	b.WriteString("Rule annotations:\n")
	keys := make([]string, 0, len(f.Annotations))
	for k := range f.Annotations {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s: %s\n", k, f.Annotations[k])
	}
	if f.Values != "" {
		b.WriteString("Current value: " + f.Values + "\n")
	} else {
		b.WriteString("Current value: unavailable (no __values__ on the alert instance)\n")
	}
	if f.Context.LogQuery != "" {
		b.WriteString("Log query: " + f.Context.LogQuery + "\n")
	}
	if len(f.Context.Logs) > 0 {
		fmt.Fprintf(&b, "Recent logs (last %d):\n", len(f.Context.Logs))
		for _, l := range f.Context.Logs {
			b.WriteString("  " + l + "\n")
		}
	} else {
		b.WriteString("Recent logs: " + f.Context.LogsNote + "\n")
	}
	if len(f.Context.Related) > 0 {
		b.WriteString("Related open beads (same rule uid): " + strings.Join(f.Context.Related, ", ") + "\n")
	} else {
		b.WriteString("Related open beads (same rule uid): none\n")
	}
	return b.String()
}

// dashboardURL picks the best link for a rule: its dashboard panel when the
// rule is linked to one, else the generator URL Grafana stamped on the
// instance, else the rule's own view page.
func dashboardURL(baseURL, ruleUID, generatorURL string, ann map[string]string) string {
	base := strings.TrimRight(baseURL, "/")
	if uid := ann["__dashboardUid__"]; uid != "" {
		u := base + "/d/" + uid
		if p := ann["__panelId__"]; p != "" {
			u += "?viewPanel=" + url.QueryEscape(p)
		}
		return u
	}
	if generatorURL != "" {
		return generatorURL
	}
	if base == "" || ruleUID == "" {
		return ""
	}
	return base + "/alerting/grafana/" + ruleUID + "/view"
}

// publicAnnotations returns the rule's annotations verbatim minus Grafana's
// internal "__"-prefixed keys (__values__ is rendered as the current value).
func publicAnnotations(raw map[string]string) map[string]string {
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if strings.HasPrefix(k, "__") {
			continue
		}
		out[k] = v
	}
	return out
}

// lokiClient is a minimal read-only Loki query client. httpClient MUST
// carry an explicit timeout.
type lokiClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// recentLines runs the LogQL query over the last logLookback and returns up
// to limit lines, newest first.
func (c *lokiClient) recentLines(ctx context.Context, query string, limit int, now time.Time) ([]string, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("direction", "backward")
	q.Set("start", strconv.FormatInt(now.Add(-logLookback).UnixNano(), 10))
	q.Set("end", strconv.FormatInt(now.UnixNano(), 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(c.baseURL, "/")+"/loki/api/v1/query_range?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("loki: build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("loki: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("loki: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki: unexpected status %d: %s", resp.StatusCode, string(body))
	}
	var parsed struct {
		Data struct {
			Result []struct {
				Values [][]string `json:"values"` // [ns-timestamp, line]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("loki: decode response: %w", err)
	}
	type entry struct {
		ts   int64
		line string
	}
	var entries []entry
	for _, s := range parsed.Data.Result {
		for _, v := range s.Values {
			if len(v) < 2 {
				continue
			}
			ts, _ := strconv.ParseInt(v[0], 10, 64)
			entries = append(entries, entry{ts, v[1]})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ts > entries[j].ts })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, e.line)
	}
	return lines, nil
}
