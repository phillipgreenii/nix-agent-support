package ccaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	clusterGap   = 10 * time.Second
	maxSamples   = 5
	maxSigSample = 500
)

type key struct{ kind, subject string }

type acc struct {
	typ      string
	query    string
	qidx     int
	metrics  map[string]float64
	first    time.Time
	last     time.Time
	head     string
	samples  map[string]bool
	costOnly bool
}

type board struct {
	scope, from, to string
	m               map[key]*acc
	costs           map[string]*acc // by subject key, unmatched cost rows
}

func newBoard(scope, from, to string) *board {
	return &board{scope: scope, from: from, to: to, m: map[key]*acc{}, costs: map[string]*acc{}}
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func (b *board) get(kind, subject, typ, query string) *acc {
	k := key{kind, subject}
	a := b.m[k]
	if a == nil {
		a = &acc{typ: typ, query: query, qidx: queryIndex(query), metrics: map[string]float64{}, samples: map[string]bool{}}
		b.m[k] = a
	} else if qi := queryIndex(query); qi < a.qidx {
		a.query, a.qidx = query, qi
	}
	return a
}

func (a *acc) addMetric(name string, v float64) { a.metrics[name] += v }

func (a *acc) seen(t time.Time) {
	if t.IsZero() {
		return
	}
	if a.first.IsZero() || t.Before(a.first) {
		a.first = t
	}
	if a.last.IsZero() || t.After(a.last) {
		a.last = t
	}
}

func (a *acc) sample(s string) { a.samples[s] = true }

func ref(session, path, seq string) string {
	return fmt.Sprintf("session=%s path=%s seq=%s", session, path, seq)
}

func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(t)
	}
}

func num(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

func setNum(a *acc, name string, v any) {
	if f, ok := num(v); ok {
		a.addMetric(name, f)
	}
}

func (b *board) ingest(q string, rows []map[string]any) {
	switch q {
	case "error-rate-by-tool":
		for _, r := range rows {
			tool := str(r["tool_name"])
			if tool == "" {
				tool = "unknown"
			}
			a := b.get("error-rate", "tool:"+tool, "tool", q)
			setNum(a, "calls", r["calls"])
			setNum(a, "errors", r["errors"])
			setNum(a, "error_pct", r["error_pct"])
		}
	case "top-signatures":
		for _, r := range rows {
			sig := str(r["signature"])
			a := b.get("error", SigKey(sig), "signature", q)
			a.head = "signature: " + clip(sig)
			setNum(a, "errors", r["errors"])
			setNum(a, "sessions", r["sessions"])
			setNum(a, "subagent", r["subagent"])
		}
	case "cost-by-signature":
		for _, r := range rows {
			sig := str(r["signature"])
			a := &acc{typ: "signature", query: q, qidx: queryIndex(q), metrics: map[string]float64{}, samples: map[string]bool{}, head: "signature: " + clip(sig)}
			// elapsed_ms_sum, never duration_ms_sum (near zero by design).
			for _, c := range []string{"elapsed_ms_sum", "elapsed_ms_mean", "elapsed_ms_p50", "elapsed_ms_max", "idle_calls", "idle_ms_sum"} {
				setNum(a, c, r[c])
			}
			b.costs[SigKey(sig)] = a
		}
	case "retry-chains":
		b.retry(rows)
	case "denied-tool-calls":
		b.denied(rows)
	case "interruptions":
		b.interruptions(rows)
	case "hook-refusals-in-body":
		b.refusals(rows)
	}
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > maxSigSample {
		return string(r[:maxSigSample])
	}
	return s
}

func (b *board) retry(rows []map[string]any) {
	type g struct {
		sig      string
		chains   map[string]bool
		sessions map[string]bool
		refs     []string
	}
	groups := map[string]*g{}
	for _, r := range rows {
		sig := str(r["signature"])
		k := SigKey(sig)
		x := groups[k]
		if x == nil {
			x = &g{sig: sig, chains: map[string]bool{}, sessions: map[string]bool{}}
			groups[k] = x
		}
		x.chains[str(r["path"])+"\x00"+str(r["failed_seq"])] = true
		x.sessions[str(r["session_id"])] = true
		x.refs = append(x.refs, ref(str(r["session_id"]), str(r["path"]), str(r["failed_seq"])))
	}
	for k, x := range groups {
		a := b.get("retry", k, "signature", "retry-chains")
		a.head = "signature: " + clip(x.sig)
		a.addMetric("retry_chains", float64(len(x.chains)))
		a.addMetric("retry_sessions", float64(len(x.sessions)))
		for _, s := range x.refs {
			a.sample(s)
		}
	}
}

func (b *board) denied(rows []map[string]any) {
	type g struct {
		total, sub float64
		kinds      map[string]float64
		sessions   map[string]bool
		first      time.Time
		last       time.Time
		refs       []string
	}
	groups := map[string]*g{}
	for _, r := range rows {
		lead := str(r["lead_cmd"])
		if lead == "" {
			lead = str(r["tool_name"])
		}
		if lead == "" {
			lead = "unknown"
		}
		k := "lead:" + lead
		x := groups[k]
		if x == nil {
			x = &g{kinds: map[string]float64{}, sessions: map[string]bool{}}
			groups[k] = x
		}
		x.total++
		if f, ok := num(r["is_sidechain"]); ok && f == 1 {
			x.sub++
		}
		x.kinds[strings.ReplaceAll(str(r["kind"]), "-", "_")]++
		x.sessions[str(r["session_id"])] = true
		if t := parseTime(str(r["ts"])); !t.IsZero() {
			if x.first.IsZero() || t.Before(x.first) {
				x.first = t
			}
			if t.After(x.last) {
				x.last = t
			}
		}
		x.refs = append(x.refs, ref(str(r["session_id"]), str(r["path"]), str(r["seq"])))
	}
	for k, x := range groups {
		a := b.get("denied", k, "lead", "denied-tool-calls")
		a.addMetric("denied", x.total)
		a.addMetric("denied_subagent", x.sub)
		a.addMetric("denied_sessions", float64(len(x.sessions)))
		for kind, n := range x.kinds {
			a.addMetric("denied_"+kind, n)
		}
		a.seen(x.first)
		a.seen(x.last)
		for _, s := range x.refs {
			a.sample(s)
		}
	}
}

type intr struct {
	session, path, seq string
	ts                 time.Time
	calls              float64
}

func (b *board) interruptions(rows []map[string]any) {
	bySession := map[string][]intr{}
	for _, r := range rows {
		s := str(r["session_id"])
		c, _ := num(r["prev_tool_calls"])
		bySession[s] = append(bySession[s], intr{s, str(r["path"]), str(r["seq"]), parseTime(str(r["ts"])), c})
	}
	for sid, items := range bySession {
		sort.SliceStable(items, func(i, j int) bool {
			a, c := items[i], items[j]
			if !a.ts.Equal(c.ts) {
				return a.ts.Before(c.ts)
			}
			if a.path != c.path {
				return a.path < c.path
			}
			return a.seq < c.seq
		})
		var cur []intr
		flush := func() {
			if len(cur) == 0 {
				return
			}
			h := sha256.Sum256([]byte(sid + "\x00" + cur[0].path + "\x00" + cur[0].seq))
			a := b.get("interruption", "session:"+hex.EncodeToString(h[:])[:8], "session", "interruptions")
			a.addMetric("interruptions", float64(len(cur)))
			for _, it := range cur {
				a.addMetric("prev_tool_calls", it.calls)
				a.seen(it.ts)
				a.sample(ref(it.session, it.path, it.seq))
			}
			if !a.first.IsZero() {
				a.metrics["span_ms"] = float64(a.last.Sub(a.first) / time.Millisecond)
			}
			cur = nil
		}
		for _, it := range items {
			if n := len(cur); n > 0 {
				prev := cur[n-1]
				if prev.ts.IsZero() || it.ts.IsZero() || it.ts.Sub(prev.ts) > clusterGap {
					flush()
				}
			}
			cur = append(cur, it)
		}
		flush()
	}
}

func (b *board) refusals(rows []map[string]any) {
	type g struct {
		sig      string
		n        float64
		sessions map[string]bool
		first    time.Time
		last     time.Time
		refs     []string
	}
	groups := map[string]*g{}
	for _, r := range rows {
		sig := str(r["signature"])
		k := SigKey(sig)
		x := groups[k]
		if x == nil {
			x = &g{sig: sig, sessions: map[string]bool{}}
			groups[k] = x
		}
		x.n++
		x.sessions[str(r["session_id"])] = true
		if t := parseTime(str(r["ts"])); !t.IsZero() {
			if x.first.IsZero() || t.Before(x.first) {
				x.first = t
			}
			if t.After(x.last) {
				x.last = t
			}
		}
		x.refs = append(x.refs, ref(str(r["session_id"]), str(r["path"]), str(r["seq"])))
	}
	for k, x := range groups {
		a := b.get("error", k, "signature", "hook-refusals-in-body")
		if a.head == "" {
			a.head = "signature: " + clip(x.sig)
		}
		a.addMetric("hook_refusals", x.n)
		a.addMetric("hook_refusal_sessions", float64(len(x.sessions)))
		a.seen(x.first)
		a.seen(x.last)
		for _, s := range x.refs {
			a.sample(s)
		}
	}
}

// finish merges cost rows into the matching signature row. A cost row with no
// matching error row is dropped when top-signatures ran in the same call (it
// is outside the ranked set) and otherwise becomes its own error row.
func (b *board) finish(topRan bool) {
	for k, c := range b.costs {
		if a := b.m[key{"error", k}]; a != nil {
			for n, v := range c.metrics {
				a.metrics[n] = v
			}
			continue
		}
		if !topRan {
			b.m[key{"error", k}] = c
		}
	}
}

func (b *board) rows() []Row {
	out := make([]Row, 0, len(b.m))
	for k, a := range b.m {
		first, last := b.from, b.to
		if !a.first.IsZero() {
			first = a.first.Format(boundLayout)
			last = a.last.Format(boundLayout)
		}
		samples := make([]string, 0, len(a.samples)+1)
		for s := range a.samples {
			samples = append(samples, s)
		}
		sort.Strings(samples)
		if len(samples) > maxSamples {
			samples = samples[:maxSamples]
		}
		if a.head != "" {
			samples = append([]string{a.head}, samples...)
		}
		out = append(out, Row{
			SchemaVersion: 1, SubjectKey: k.subject, Type: a.typ, Kind: k.kind,
			Scope: b.scope, Window: Window{From: b.from, To: b.to},
			Metrics: a.metrics, Baseline: []any{}, FirstSeen: first, LastSeen: last,
			Query: a.query, Samples: samples,
		})
	}
	sortRows(out)
	for i := range out {
		out[i].ID = fmt.Sprintf("E-%03d", i+1)
	}
	return out
}
