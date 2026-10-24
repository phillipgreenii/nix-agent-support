package prchange

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// docRel is the behavior doc that publishes the exit-code table, relative to
// this package.
var docRel = filepath.Join("..", "..", "..", "..", "docs", "behavior", "pg-decider", "pr-change.md")

var docRow = regexp.MustCompile("(?m)^\\|\\s*`(\\d+)`\\s*\\|\\s*`(\\w+)`\\s*\\|\\s*`(\\w+)`\\s*\\|\\s*(yes|no)\\s*\\|")

// TestPublishedTableMatchesTheCode compares the table in the behavior doc with
// Table row for row, so neither can change alone. The nix build sandbox holds
// only this module, so the check skips there and runs at commit time.
func TestPublishedTableMatchesTheCode(t *testing.T) {
	b, err := os.ReadFile(docRel)
	if err != nil {
		t.Skipf("behavior doc not reachable (sandbox): %v", err)
	}
	type row struct {
		stage, class string
		retry        bool
	}
	docRows := map[int]row{}
	for _, m := range docRow.FindAllStringSubmatch(string(b), -1) {
		code, _ := strconv.Atoi(m[1])
		if _, dup := docRows[code]; dup {
			t.Errorf("code %d is listed twice in the doc", code)
		}
		docRows[code] = row{m[2], m[3], m[4] == "yes"}
	}
	if len(docRows) == 0 {
		t.Fatal("non-vacuity: no table rows found in the doc")
	}
	for _, s := range Table {
		got, ok := docRows[s.Code]
		if !ok {
			t.Errorf("code %d (%s/%s) is not in the doc", s.Code, s.Stage, s.Class)
			continue
		}
		if got.stage != s.Stage || got.class != s.Class || got.retry != s.Retryable {
			t.Errorf("code %d: doc says %+v, code says %s/%s retryable=%v", s.Code, got, s.Stage, s.Class, s.Retryable)
		}
		delete(docRows, s.Code)
	}
	for code, r := range docRows {
		t.Errorf("the doc lists code %d (%+v) the code does not have", code, r)
	}
}
