package integration

import (
	"errors"
	"strings"
	"testing"
)

// These unit tests are NOT guarded by the integration skip: they pin the
// skip reasons and the one visible SKIPPED line format.

func TestSkipReasonFor(t *testing.T) {
	ok := hostFacts{goos: "darwin", caffeinateFound: true, probeVisible: true}
	cases := []struct {
		name  string
		facts func(hostFacts) hostFacts
		want  string
	}{
		{"runs", func(f hostFacts) hostFacts { return f }, ""},
		{"forced", func(f hostFacts) hostFacts { f.forced = true; return f }, "forced by PG_KEEP_AWAKE_ITEST_SKIP"},
		{"not darwin", func(f hostFacts) hostFacts { f.goos = "linux"; return f }, "GOOS is linux, not darwin"},
		{"no caffeinate", func(f hostFacts) hostFacts { f.caffeinateFound = false; return f }, "/usr/bin/caffeinate not found"},
		{
			"pmset fails", func(f hostFacts) hostFacts { f.pmsetErr = errors.New("exit status 1"); return f },
			"pmset -g assertions failed: exit status 1",
		},
		{
			"not visible", func(f hostFacts) hostFacts { f.probeVisible = false; return f },
			"caffeinate assertions are not visible to pmset here",
		},
		{
			"forced wins over linux", func(f hostFacts) hostFacts { f.forced, f.goos = true, "linux"; return f },
			"forced by PG_KEEP_AWAKE_ITEST_SKIP",
		},
	}
	for _, c := range cases {
		if got := skipReasonFor(c.facts(ok)); got != c.want {
			t.Errorf("%s: reason %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSkipLineFormat(t *testing.T) {
	if got := skipLine(""); got != "" {
		t.Errorf("an empty reason yields %q, want no line", got)
	}
	got := skipLine("GOOS is linux, not darwin")
	if want := "SKIPPED integration: GOOS is linux, not darwin"; got != want {
		t.Errorf("line = %q, want %q", got, want)
	}
	if strings.Contains(got, "\n") || !strings.HasPrefix(got, "SKIPPED integration: ") {
		t.Errorf("line %q must be one line starting SKIPPED integration: ", got)
	}
}
