package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// The fixtures below are FABRICATED but follow the live payload shape
// documented in the design (9.2): a column-oriented state-history frame
// `{data:{values:[times, texts, prevs, nexts, datas]}}`, times in
// microseconds since the epoch, and each row's label set embedded in the
// text field as `<rule title> {k=v, ...} - <data string>`.

var (
	day     = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	winFrom = day
	winTo   = day.Add(24 * time.Hour)
)

type histRow struct {
	at         time.Time
	text       string
	prev, next string
}

func us(t time.Time) int64 { return t.UnixMicro() }

func mustJSON(t testing.TB, v any) json.RawMessage {
	if t != nil {
		t.Helper()
	}
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// mkFrame builds a well-formed frame from rows (an empty call is the
// all-empty-columns frame a rule with no history answers).
func mkFrame(rows ...histRow) apiHistory {
	times, texts, prevs, nexts, datas := []int64{}, []string{}, []string{}, []string{}, []string{}
	for _, r := range rows {
		times = append(times, us(r.at))
		texts = append(texts, r.text)
		prevs = append(prevs, r.prev)
		nexts = append(nexts, r.next)
		datas = append(datas, "{}")
	}
	return rawFrame(mustJSON(nil, times), mustJSON(nil, texts), mustJSON(nil, prevs), mustJSON(nil, nexts), mustJSON(nil, datas))
}

func rawFrame(cols ...json.RawMessage) apiHistory {
	h := apiHistory{Data: &struct {
		Values []json.RawMessage `json:"values"`
	}{Values: cols}}
	return h
}

func histText(title, labels string) string {
	return title + " {" + labels + "} - {}"
}

func TestListHistory_NormalEpisodes(t *testing.T) {
	const labelsA = "host_name=web-1, mountpoint=/var, severity=critical"
	const labelsB = "host_name=web-2, mountpoint=/var, severity=warning"
	textA := histText("DiskFull", labelsA)
	textB := histText("DiskFull", labelsB)
	st := &stubTransport{
		rules: []apiRule{{UID: "uid-disk", Title: "DiskFull"}},
		frames: map[string]apiHistory{"uid-disk": mkFrame(
			// Deliberately out of time order: the backend sorts.
			histRow{day.Add(5 * time.Hour), textA, "Normal", "Alerting"},
			histRow{day.Add(1 * time.Hour), textA, "Normal", "Alerting"},
			histRow{day.Add(2 * time.Hour), textA, "Alerting", "Normal"},
			histRow{day.Add(3 * time.Hour), textB, "Normal", "Alerting"},
			histRow{day.Add(6 * time.Hour), textA, "Alerting", "Normal"},
		)},
	}
	res, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	if res.Truncated {
		t.Error("Truncated = true for a short frame")
	}
	if len(res.Episodes) != 3 {
		t.Fatalf("episodes = %+v, want 3", res.Episodes)
	}

	first, second, third := res.Episodes[0], res.Episodes[1], res.Episodes[2]
	want := schema.AlertEpisode{
		RuleID:    "uid-disk",
		Title:     "DiskFull",
		StartedAt: "2026-10-01T01:00:00Z",
		EndedAt:   "2026-10-01T02:00:00Z",
		Severity:  schema.SeverityCritical,
		Attributes: map[string]string{
			"label.host_name":          "web-1",
			"label.mountpoint":         "/var",
			"label.severity":           "critical",
			"label.alertname":          "DiskFull",
			"label.__alert_rule_uid__": "uid-disk",
		},
	}
	if !reflect.DeepEqual(first, want) {
		t.Errorf("first = %+v\nwant   %+v", first, want)
	}
	// Other label set, still alerting at the end of the window: no ended_at.
	if second.StartedAt != "2026-10-01T03:00:00Z" || second.EndedAt != "" || second.Severity != schema.SeverityMedium ||
		second.Attributes["label.host_name"] != "web-2" {
		t.Errorf("second = %+v", second)
	}
	// Same label set as first, a fresh episode after the first closed.
	if third.StartedAt != "2026-10-01T05:00:00Z" || third.EndedAt != "2026-10-01T06:00:00Z" ||
		third.Attributes["label.host_name"] != "web-1" {
		t.Errorf("third = %+v", third)
	}

	// One history call for the one rule, with the window and the limit.
	if len(st.historyCalls) != 1 || st.rulesCalls != 1 {
		t.Fatalf("rulesCalls=%d historyCalls=%+v", st.rulesCalls, st.historyCalls)
	}
	c := st.historyCalls[0]
	if c.uid != "uid-disk" || !c.from.Equal(winFrom) || !c.to.Equal(winTo) || c.limit != historyLimit {
		t.Errorf("history call = %+v", c)
	}
}

func TestListHistory_OneHistoryCallPerRuleConcatenated(t *testing.T) {
	st := &stubTransport{
		rules: []apiRule{{UID: "r1", Title: "One"}, {UID: "r2", Title: "Two"}, {UID: "r1", Title: "One dup"}, {UID: "", Title: "no uid"}},
		frames: map[string]apiHistory{
			"r1": mkFrame(histRow{day.Add(4 * time.Hour), histText("One", "a=1"), "Normal", "Alerting"}),
			"r2": mkFrame(histRow{day.Add(2 * time.Hour), histText("Two", "b=2"), "Normal", "Alerting"}),
		},
	}
	res, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
	if err != nil {
		t.Fatal(err)
	}
	var uids []string
	for _, c := range st.historyCalls {
		uids = append(uids, c.uid)
	}
	if !reflect.DeepEqual(uids, []string{"r1", "r2"}) {
		t.Fatalf("history calls = %v, want one per distinct non-empty rule uid", uids)
	}
	if len(res.Episodes) != 2 || res.Episodes[0].RuleID != "r2" || res.Episodes[1].RuleID != "r1" {
		t.Fatalf("episodes = %+v, want r2 (02:00) then r1 (04:00)", res.Episodes)
	}
}

func TestListHistory_RuleWithNoHistory(t *testing.T) {
	for name, frame := range map[string]apiHistory{
		"empty columns":    mkFrame(),
		"no columns":       rawFrame(),
		"no alerting rows": mkFrame(histRow{day.Add(time.Hour), histText("Q", "a=1"), "Normal", "Pending"}),
	} {
		t.Run(name, func(t *testing.T) {
			st := &stubTransport{
				rules:  []apiRule{{UID: "quiet", Title: "Quiet"}},
				frames: map[string]apiHistory{"quiet": frame},
			}
			res, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
			if err != nil {
				t.Fatalf("ListHistory: %v", err)
			}
			if res.Episodes == nil || len(res.Episodes) != 0 || res.Truncated {
				t.Fatalf("res = %+v, want empty non-nil episodes (reachable and empty is success)", res)
			}
			b, _ := json.Marshal(res)
			if !strings.Contains(string(b), `"episodes":[]`) {
				t.Errorf("wire form %s must render episodes as []", b)
			}
		})
	}
}

func TestListHistory_NoRulesAtAll(t *testing.T) {
	res, err := New(&stubTransport{}).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
	if err != nil || len(res.Episodes) != 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestListHistory_MalformedTextFieldDegradesWithoutPanic(t *testing.T) {
	at := day.Add(time.Hour)
	texts := []json.RawMessage{
		mustJSON(t, "no braces here at all"),
		json.RawMessage(`null`),
		json.RawMessage(`42`),
		json.RawMessage(`{"obj":true}`),
		mustJSON(t, "Rule {unterminated=1, a=2"),
		mustJSON(t, "Rule {=novalue, bare, ok=1} - {}"),
		mustJSON(t, "}{ reversed } {"),
		mustJSON(t, ""),
	}
	n := len(texts)
	times, prevs, nexts := make([]int64, n), make([]string, n), make([]string, n)
	for i := range texts {
		times[i] = us(at.Add(time.Duration(i) * time.Minute))
		prevs[i], nexts[i] = "Normal", "Alerting"
	}
	frame := rawFrame(mustJSON(t, times), mustJSON(t, texts), mustJSON(t, prevs), mustJSON(t, nexts), json.RawMessage(`[]`))
	st := &stubTransport{
		rules:  []apiRule{{UID: "u", Title: "Rule"}},
		frames: map[string]apiHistory{"u": frame},
	}
	res, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
	if err != nil {
		t.Fatalf("ListHistory: %v (a malformed text field must degrade, not fail)", err)
	}
	// Unparseable rows are not dropped. Rows whose text is identical (null,
	// number, object and "" all decode to "") share one label-set key, so they
	// collapse into a single episode; distinct unparseable texts stay distinct.
	if len(res.Episodes) != 5 {
		t.Fatalf("episodes = %d, want 5 (4 distinct unparsed texts + 1 with a recoverable block)", len(res.Episodes))
	}
	for _, ep := range res.Episodes {
		if ep.RuleID != "u" || ep.Title != "Rule" || ep.Attributes["label.__alert_rule_uid__"] != "u" {
			t.Errorf("degraded episode lost rule-level identity: %+v", ep)
		}
	}
	// The one row with a recoverable block keeps its good pair and drops the
	// bad ones.
	var found bool
	for _, ep := range res.Episodes {
		if ep.Attributes["label.ok"] == "1" {
			found = true
			if _, bad := ep.Attributes["label.bare"]; bad {
				t.Errorf("pair without '=' leaked: %+v", ep.Attributes)
			}
			if _, bad := ep.Attributes["label."]; bad {
				t.Errorf("empty key leaked: %+v", ep.Attributes)
			}
		}
	}
	if !found {
		t.Errorf("recoverable pair lost: %+v", res.Episodes)
	}
}

func TestParseHistoryLabels(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		want       map[string]string
		wantParsed bool
	}{
		{"typical", "DiskFull {a=1, b=two words, c=x=y} - {x}", map[string]string{"a": "1", "b": "two words", "c": "x=y"}, true},
		{"empty block", "T {} - {}", map[string]string{}, true},
		{"no block", "T", map[string]string{}, false},
		{"unterminated", "T {a=1", map[string]string{}, false},
		{"empty", "", map[string]string{}, false},
		{"first block only", "T {a=1} - {b=2}", map[string]string{"a": "1"}, true},
		{"empty value kept", "T {a=}", map[string]string{"a": ""}, true},
		{"unicode", "T {n=héllo, k=日本}", map[string]string{"n": "héllo", "k": "日本"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, parsed := parseHistoryLabels(tc.text)
			if parsed != tc.wantParsed || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseHistoryLabels(%q) = %v, %v; want %v, %v", tc.text, got, parsed, tc.want, tc.wantParsed)
			}
		})
	}
}

func TestListHistory_MalformedFrameIsUnavailable(t *testing.T) {
	good := mustJSON(t, []int64{us(day)})
	str := mustJSON(t, []string{"x"})
	frames := map[string]apiHistory{
		"no data object":    {},
		"too few columns":   rawFrame(good, str),
		"times not numbers": rawFrame(mustJSON(t, []string{"yesterday"}), str, str, str),
		"times not array":   rawFrame(json.RawMessage(`"x"`), str, str, str),
		"ragged columns":    rawFrame(good, mustJSON(t, []string{"a", "b"}), str, str),
		"states not text":   rawFrame(good, str, mustJSON(t, []int{1}), str),
	}
	for name, frame := range frames {
		t.Run(name, func(t *testing.T) {
			st := &stubTransport{rules: []apiRule{{UID: "u", Title: "T"}}, frames: map[string]apiHistory{"u": frame}}
			_, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
			wantErrIs(t, err, scriptout.ErrUnavailable)
		})
	}
}

func TestListHistory_UnreachableIsUnavailable(t *testing.T) {
	dead := errors.New("grafana unreachable: connection refused")

	t.Run("rule enumeration fails", func(t *testing.T) {
		_, err := New(&stubTransport{rulesErr: dead}).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
		wantErrIs(t, err, scriptout.ErrUnavailable)
	})
	t.Run("a per-rule history call fails", func(t *testing.T) {
		st := &stubTransport{
			rules:   []apiRule{{UID: "r1", Title: "One"}, {UID: "r2", Title: "Two"}},
			histErr: map[string]error{"r2": dead},
		}
		res, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
		wantErrIs(t, err, scriptout.ErrUnavailable)
		if res != nil {
			t.Errorf("a partial result must not be returned: %+v", res)
		}
	})
}

func TestListHistory_TruncatedWhenRuleHitsLimit(t *testing.T) {
	rows := make([]histRow, 0, historyLimit)
	for i := 0; i < historyLimit; i++ {
		rows = append(rows, histRow{day.Add(time.Duration(i) * time.Second), fmt.Sprintf("T {i=%d}", i), "Normal", "Alerting"})
	}
	st := &stubTransport{rules: []apiRule{{UID: "busy", Title: "Busy"}}, frames: map[string]apiHistory{"busy": mkFrame(rows...)}}
	res, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Episodes) != historyLimit {
		t.Fatalf("Truncated=%v episodes=%d", res.Truncated, len(res.Episodes))
	}
}

func TestListHistory_Query(t *testing.T) {
	st := &stubTransport{
		rules: []apiRule{{UID: "r1", Title: "DiskFull"}, {UID: "r2", Title: "Slow"}},
		frames: map[string]apiHistory{
			"r1": mkFrame(
				histRow{day.Add(1 * time.Hour), histText("DiskFull", "severity=critical, host_name=a"), "Normal", "Alerting"},
				histRow{day.Add(2 * time.Hour), histText("DiskFull", "severity=warning, host_name=b"), "Normal", "Alerting"},
			),
			"r2": mkFrame(histRow{day.Add(3 * time.Hour), histText("Slow", "severity=warning, host_name=c"), "Normal", "Alerting"}),
		},
	}
	hosts := func(q schema.QueryExpr) []string {
		t.Helper()
		res, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, q)
		if err != nil {
			t.Fatalf("query %v: %v", q, err)
		}
		var out []string
		for _, ep := range res.Episodes {
			out = append(out, ep.Attributes["label.host_name"])
		}
		return out
	}
	tests := []struct {
		name string
		q    schema.QueryExpr
		want []string
	}{
		{"empty query is everything", nil, []string{"a", "b", "c"}},
		{"empty set is everything", schema.QueryExpr{"{}"}, []string{"a", "b", "c"}},
		{"equality", schema.QueryExpr{`{severity="critical"}`}, []string{"a"}},
		{"inequality", schema.QueryExpr{`{severity!="critical"}`}, []string{"b", "c"}},
		{"regex is anchored", schema.QueryExpr{`{host_name=~"a|c"}`}, []string{"a", "c"}},
		{"regex does not partial-match", schema.QueryExpr{`{host_name=~"a|"}`}, []string{"a"}},
		{"negative regex", schema.QueryExpr{`{host_name!~"a|b"}`}, []string{"c"}},
		{"matchers are ANDed", schema.QueryExpr{`{severity="warning", host_name="c"}`}, []string{"c"}},
		{"sets are ORed", schema.QueryExpr{`{host_name="a"}`, `{host_name="c"}`}, []string{"a", "c"}},
		{"rule-level alertname is matchable", schema.QueryExpr{`{alertname="Slow"}`}, []string{"c"}},
		{"missing label reads empty", schema.QueryExpr{`{nope=""}`}, []string{"a", "b", "c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hosts(tc.q); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hosts = %v, want %v", got, tc.want)
			}
		})
	}

	for _, bad := range []string{`{severity}`, `{severity=~"("}`, `{a="x"`} {
		_, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, schema.QueryExpr{bad})
		wantErrIs(t, err, scriptout.ErrInvalidArgument)
	}
}

func TestListHistory_InvalidWindowAndConfig(t *testing.T) {
	_, err := New(&stubTransport{}).ListHistory(cfgCtx(t, baseCfg()), winTo, winFrom, nil)
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
	_, err = New(&stubTransport{}).ListHistory(context.Background(), winFrom, winTo, nil)
	wantErrIs(t, err, scriptout.ErrInvalidArgument)
}

func TestListHistory_RuleTitleFallbacks(t *testing.T) {
	row := func(text string) apiHistory {
		return mkFrame(histRow{day.Add(time.Hour), text, "Normal", "Alerting"})
	}
	st := &stubTransport{
		rules: []apiRule{{UID: "u1"}, {UID: "u2"}},
		frames: map[string]apiHistory{
			"u1": row("x {alertname=FromText}"),
			"u2": row("x {a=1}"),
		},
	}
	res, err := New(st).ListHistory(cfgCtx(t, baseCfg()), winFrom, winTo, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Episodes[0].Title != "FromText" || res.Episodes[1].Title != "u2" {
		t.Fatalf("titles = %q, %q", res.Episodes[0].Title, res.Episodes[1].Title)
	}
}

// Design 4 / 9.2: attention (and list/show) MUST stay stateless and
// history-free.
func TestAttentionListAndShowNeverTouchTheHistoryPath(t *testing.T) {
	st := &stubTransport{
		all:   []apiAlert{mkAlert("a", "A", "critical", "active")},
		rules: []apiRule{{UID: "r", Title: "R"}},
	}
	b := New(st)
	ctx := cfgCtx(t, baseCfg())
	if _, err := b.ListAttention(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.List(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Show(ctx, "grafana:a"); err != nil {
		t.Fatal(err)
	}
	if st.rulesCalls != 0 || len(st.historyCalls) != 0 {
		t.Fatalf("history path was called: rules=%d history=%+v", st.rulesCalls, st.historyCalls)
	}
}
