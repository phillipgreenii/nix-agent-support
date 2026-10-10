package unstick

import (
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 10, 8, 9, 10, 123456789, time.UTC)

func TestNewMarker_validAndRoundTrip(t *testing.T) {
	tests := []struct {
		name, outcome, reason, recheck, want string
	}{
		{"date", "unchanged", "waiting on design", "2026-12-01", "[unstick 2026-10-10T08:09:10Z] unchanged: waiting on design; recheck-when: 2026-12-01"},
		{"on-change", "de-labelled", "operator input needed", "on-change", "[unstick 2026-10-10T08:09:10Z] de-labelled: operator input needed; recheck-when: on-change"},
		{"closes simple", "fixed-deps", "edge retargeted", "tc-abc closes", "[unstick 2026-10-10T08:09:10Z] fixed-deps: edge retargeted; recheck-when: tc-abc closes"},
		{"closes multi-hyphen", "unchanged", "x", "tc-mol-4prt closes", "[unstick 2026-10-10T08:09:10Z] unchanged: x; recheck-when: tc-mol-4prt closes"},
		{"closes dotted", "unchanged", "x", "tc-o14i5.3.7 closes", "[unstick 2026-10-10T08:09:10Z] unchanged: x; recheck-when: tc-o14i5.3.7 closes"},
		{"reason with punctuation", "released", "dead claim: lease expired (2h), see tc-1", "on-change", "[unstick 2026-10-10T08:09:10Z] released: dead claim: lease expired (2h), see tc-1; recheck-when: on-change"},
		{"non-UTC now is converted", "unchanged", "x", "on-change", "[unstick 2026-10-10T08:09:10Z] unchanged: x; recheck-when: on-change"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := fixedNow
			if tt.name == "non-UTC now is converted" {
				now = fixedNow.In(time.FixedZone("p", -7*3600))
			}
			m, err := NewMarker(now, tt.outcome, tt.reason, tt.recheck)
			if err != nil {
				t.Fatal(err)
			}
			if got := m.String(); got != tt.want {
				t.Fatalf("String =\n %q\nwant\n %q", got, tt.want)
			}
			back, err := ParseMarker(m.String())
			if err != nil {
				t.Fatalf("round trip parse: %v", err)
			}
			if back.String() != m.String() || !back.Time.Equal(m.Time) || back.Outcome != tt.outcome || back.Reason != tt.reason {
				t.Errorf("round trip mismatch: %+v vs %+v", back, m)
			}
		})
	}
}

func TestNewMarker_rejects(t *testing.T) {
	tests := []struct {
		name, outcome, reason, recheck, wantErr string
	}{
		{"empty outcome", "", "r", "on-change", "outcome"},
		{"uppercase outcome", "Closed", "r", "on-change", "outcome"},
		{"digit outcome", "a1", "r", "on-change", "outcome"},
		{"leading hyphen outcome", "-a", "r", "on-change", "outcome"},
		{"newline outcome", "a\nb", "r", "on-change", "outcome"},
		{"colon outcome", "a:b", "r", "on-change", "outcome"},
		{"empty reason", "a", "", "on-change", "reason"},
		{"blank reason", "a", "   ", "on-change", "reason"},
		{"padded reason", "a", " r", "on-change", "whitespace"},
		{"newline reason", "a", "line1\nline2", "on-change", "control"},
		{"CR reason", "a", "x\ry", "on-change", "control"},
		{"tab reason", "a", "x\ty", "on-change", "control"},
		{"backtick", "a", "use `x`", "on-change", "plain text"},
		{"dollar", "a", "cost $5", "on-change", "plain text"},
		{"single quote", "a", "it's", "on-change", "plain text"},
		{"double quote", "a", `say "hi"`, "on-change", "plain text"},
		{"embedded recheck key", "a", "x; recheck-when: y", "on-change", "recheck-when"},
		{"recheck key no space", "a", "x; recheck-when:y", "on-change", "recheck-when"},
		{"bad recheck", "a", "r", "tomorrow", "recheck-when"},
		{"bad date", "a", "r", "2026-13-40", "recheck-when"},
		{"datetime recheck", "a", "r", "2026-12-01T00:00:00Z", "recheck-when"},
		{"empty recheck", "a", "r", "", "recheck-when"},
		{"closes without id", "a", "r", " closes", "bead id"},
		{"closes with space id", "a", "r", "tc 1 closes", "bead id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewMarker(fixedNow, tt.outcome, tt.reason, tt.recheck)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseMarker_table(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantErr string
	}{
		{"valid", "[unstick 2026-10-10T08:09:10Z] unchanged: r; recheck-when: on-change", ""},
		{"surrounding whitespace", "  [unstick 2026-10-10T08:09:10Z] unchanged: r; recheck-when: on-change \t", ""},
		{"date-only timestamp", "[unstick 2026-10-10] unchanged: r; recheck-when: on-change", "timestamp"},
		{"non-Z offset", "[unstick 2026-10-10T08:09:10+00:00] unchanged: r; recheck-when: on-change", "timestamp"},
		{"fractional seconds", "[unstick 2026-10-10T08:09:10.5Z] unchanged: r; recheck-when: on-change", "timestamp"},
		{"missing recheck-when", "[unstick 2026-10-10T08:09:10Z] unchanged: r", "recheck-when"},
		{"missing outcome colon", "[unstick 2026-10-10T08:09:10Z] unchanged r; recheck-when: on-change", "outcome"},
		{"uppercase outcome", "[unstick 2026-10-10T08:09:10Z] Unchanged: r; recheck-when: on-change", "outcome"},
		{"wrong prefix", "[sweep 2026-10-10T08:09:10Z] unchanged: r; recheck-when: on-change", "prefix"},
		{"empty reason", "[unstick 2026-10-10T08:09:10Z] unchanged: ; recheck-when: on-change", "reason"},
		{"quote in reason", `[unstick 2026-10-10T08:09:10Z] unchanged: a "b"; recheck-when: on-change`, "plain text"},
		{"bad recheck value", "[unstick 2026-10-10T08:09:10Z] unchanged: r; recheck-when: soon", "recheck-when"},
		{"embedded newline", "[unstick 2026-10-10T08:09:10Z] un\nchanged: r; recheck-when: on-change", "outcome"},
		{"trailing junk after id closes", "[unstick 2026-10-10T08:09:10Z] unchanged: r; recheck-when: tc-1 closes now", "recheck-when"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseMarker(tt.line)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseMarker_fields(t *testing.T) {
	m, err := ParseMarker("[unstick 2026-10-10T08:09:10Z] fixed-deps: edge moved; recheck-when: tc-o14i5.3.7 closes")
	if err != nil {
		t.Fatal(err)
	}
	if m.Outcome != "fixed-deps" || m.Reason != "edge moved" || m.Recheck.Kind != RecheckCloses || m.Recheck.BeadID != "tc-o14i5.3.7" {
		t.Errorf("m = %+v", m)
	}
	if !m.Time.Equal(time.Date(2026, 10, 10, 8, 9, 10, 0, time.UTC)) {
		t.Errorf("time = %v", m.Time)
	}
	d, err := ParseMarker("[unstick 2026-10-10T08:09:10Z] unchanged: r; recheck-when: 2027-01-02")
	if err != nil || d.Recheck.Kind != RecheckDate || !d.Recheck.Date.Equal(time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date recheck = %+v err=%v", d.Recheck, err)
	}
	o, err := ParseMarker("[unstick 2026-10-10T08:09:10Z] unchanged: r; recheck-when: on-change")
	if err != nil || o.Recheck.Kind != RecheckOnChange {
		t.Errorf("on-change recheck = %+v err=%v", o.Recheck, err)
	}
}

func TestRow_Markers_notesAndComments(t *testing.T) {
	row := Row{
		ID: "a-1",
		Notes: "free text\n[unstick 2026-10-01T00:00:00Z] unchanged: older; recheck-when: on-change\n" +
			"[unstick 2026-10-05] unchanged: date only; recheck-when: on-change\n",
		Comments: []Comment{
			{Text: "[unstick 2026-10-03T00:00:00Z] unchanged: in comment; recheck-when: 2026-12-01"},
			{Text: "mentions [unstick inline but not at line start"},
			{Text: "[unstick 2026-10-04T00:00:00+02:00] unchanged: non z; recheck-when: on-change"},
		},
	}
	s := row.Markers()
	if len(s.Valid) != 2 || len(s.Malformed) != 2 {
		t.Fatalf("valid=%d malformed=%d: %+v", len(s.Valid), len(s.Malformed), s)
	}
	if s.Valid[0].Source != "notes" || s.Valid[1].Source != "comment" {
		t.Errorf("sources = %q, %q", s.Valid[0].Source, s.Valid[1].Source)
	}
	for _, m := range s.Malformed {
		if m.Err == nil || m.Line == "" || m.Source == "" {
			t.Errorf("malformed entry incomplete: %+v", m)
		}
	}
	newest, ok := Newest(s.Valid)
	if !ok || newest.Reason != "in comment" {
		t.Errorf("newest = %+v ok=%v", newest, ok)
	}
}

func TestNewest(t *testing.T) {
	if _, ok := Newest(nil); ok {
		t.Error("empty must report false")
	}
	mk := func(ts, reason string) Marker {
		m, err := ParseMarker("[unstick " + ts + "] unchanged: " + reason + "; recheck-when: on-change")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	// Newest by parsed time, not by order of appearance.
	got, _ := Newest([]Marker{mk("2026-10-09T00:00:00Z", "late-listed-newer"), mk("2026-10-01T00:00:00Z", "older")})
	if got.Reason != "late-listed-newer" {
		t.Errorf("got %q", got.Reason)
	}
	got, _ = Newest([]Marker{mk("2026-10-01T00:00:00Z", "older"), mk("2026-10-09T00:00:00Z", "newer")})
	if got.Reason != "newer" {
		t.Errorf("got %q", got.Reason)
	}
	// Tie: later in scan order wins.
	got, _ = Newest([]Marker{mk("2026-10-09T00:00:00Z", "first"), mk("2026-10-09T00:00:00Z", "second")})
	if got.Reason != "second" {
		t.Errorf("tie got %q", got.Reason)
	}
}

func TestScanMarkers_idsAndNewline(t *testing.T) {
	text := "[unstick 2026-10-10T00:00:00Z] unchanged: r; recheck-when: tc-mol-4prt closes\n" +
		"[unstick 2026-10-10T00:00:01Z] unchanged: r; recheck-when: tc-o14i5.3.7 closes\n" +
		"[unstick 2026-10-10T00:00:02Z] un\n"
	s := ScanMarkers(text, "notes")
	if len(s.Valid) != 2 || len(s.Malformed) != 1 {
		t.Fatalf("%+v", s)
	}
	if s.Valid[0].Recheck.BeadID != "tc-mol-4prt" || s.Valid[1].Recheck.BeadID != "tc-o14i5.3.7" {
		t.Errorf("ids = %+v", s.Valid)
	}
}
