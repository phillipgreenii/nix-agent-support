package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const (
	tBase = "0123456789abcdef0123456789abcdef01234567"
	tHead = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
)

func historyAnswer(base string, total int, hasNext bool, cursor string, oids ...string) []byte {
	nodes := make([]map[string]any, len(oids))
	for i, o := range oids {
		nodes[i] = map[string]any{"commit": map[string]any{"oid": o}}
	}
	b, _ := json.Marshal(map[string]any{"data": map[string]any{
		"rateLimit": map[string]any{"cost": 1},
		"repository": map[string]any{"pullRequest": map[string]any{
			"baseRefOid": base,
			"commits": map[string]any{
				"totalCount": total,
				"pageInfo":   map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
				"nodes":      nodes,
			},
		}},
	}})
	return b
}

func TestGetPRHistory_ReadsCommitsAndBaseTip(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return historyAnswer(tBase, 2, false, "", tHead, "bbbb"), nil
	}}
	h, err := newWriteProvider(f).GetPRHistory(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("GetPRHistory: %v", err)
	}
	if h.BaseOID != tBase || len(h.CommitSHAs) != 2 || h.CommitSHAs[0] != tHead || h.Truncated {
		t.Fatalf("history = %+v", h)
	}
	joined := strings.Join(f.calls[0], " ")
	for _, want := range []string{"api graphql", "baseRefOid", "commits(first: 100", "rateLimit { cost }", "number=7"} {
		if !strings.Contains(joined, want) {
			t.Errorf("gh call missing %q; args=%v", want, f.calls[0])
		}
	}
}

// TestGetPRHistory_PagesAndMarksATruncatedList: the cap is maxCommits; a list
// the host reports as longer is Truncated, never silently cut.
func TestGetPRHistory_PagesAndMarksATruncatedList(t *testing.T) {
	page := 0
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		page++
		oids := make([]string, 100)
		for i := range oids {
			oids[i] = fmt.Sprintf("%040x", page*1000+i)
		}
		return historyAnswer(tBase, maxCommits+500, true, fmt.Sprintf("cursor%d", page), oids...), nil
	}}
	h, err := newWriteProvider(f).GetPRHistory(context.Background(), "owner/repo", 7)
	if err != nil {
		t.Fatalf("GetPRHistory: %v", err)
	}
	if len(h.CommitSHAs) != maxCommits || !h.Truncated || page != maxCommits/100 {
		t.Fatalf("commits = %d truncated = %v pages = %d", len(h.CommitSHAs), h.Truncated, page)
	}
	if h.BaseOID != tBase {
		t.Errorf("base = %q", h.BaseOID)
	}
}

func TestGetPRHistory_UnresolvedPRAndBadInput(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return []byte(`{"data":{"rateLimit":{"cost":1},"repository":{"pullRequest":null}}}`), nil
	}}
	p := newWriteProvider(f)
	if _, err := p.GetPRHistory(context.Background(), "owner/repo", 7); err == nil || !strings.Contains(err.Error(), "Could not resolve to a PullRequest") {
		t.Errorf("err = %v, want the unresolved-PR error", err)
	}
	if _, err := p.GetPRHistory(context.Background(), "", 7); err == nil {
		t.Error("an empty repo must be an error")
	}
	if _, err := p.GetPRHistory(context.Background(), "owner/repo", 0); err == nil {
		t.Error("a zero PR number must be an error")
	}
}

type compareFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename,omitempty"`
	Patch            string `json:"patch,omitempty"`
}

func compareAnswer(files ...compareFile) []byte {
	b, _ := json.Marshal(map[string]any{"files": files})
	return b
}

func TestGetComparedFiles_ReturnsOnlyWantedFiles(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		return compareAnswer(
			compareFile{Filename: "a.go", Patch: "@@ -1 +1 @@\n-x\n+y"},
			compareFile{Filename: "b.go", Patch: "@@ -1 +1 @@\n-p\n+q"},
			compareFile{Filename: "new.go", PreviousFilename: "old.go", Patch: "@@ -1 +1 @@\n-p\n+q"},
			compareFile{Filename: "big.go"},
		), nil
	}}
	got, err := newWriteProvider(f).GetComparedFiles(context.Background(), "owner/repo", tBase, tHead, []string{"a.go", "old.go", "big.go", "absent.go"})
	if err != nil {
		t.Fatalf("GetComparedFiles: %v", err)
	}
	var names []string
	for _, c := range got {
		names = append(names, c.Path+"<"+c.PreviousPath+">")
	}
	if strings.Join(names, ",") != "a.go<>,new.go<old.go>,big.go<>" {
		t.Fatalf("files = %v", names)
	}
	if got[2].Patch != "" || got[0].Patch == "" {
		t.Errorf("patches = %+v", got)
	}
	call := strings.Join(f.calls[0], " ")
	want := fmt.Sprintf("api repos/owner/repo/compare/%s...%s?per_page=300&page=1", tBase, tHead)
	if call != want {
		t.Errorf("call = %q, want %q", call, want)
	}
	if len(f.calls) != 1 {
		t.Errorf("a short page ends the listing; calls = %d", len(f.calls))
	}
}

// TestGetComparedFiles_PagesUntilEveryWantedFileIsFound: the second page holds
// the wanted file, a third page is not read.
func TestGetComparedFiles_PagesUntilEveryWantedFileIsFound(t *testing.T) {
	page := 0
	f := &writeFake{handle: func(args []string, _ []byte) ([]byte, error) {
		page++
		files := make([]compareFile, comparePageSize)
		for i := range files {
			files[i] = compareFile{Filename: fmt.Sprintf("p%d_f%d.go", page, i)}
		}
		if page == 2 {
			files[10] = compareFile{Filename: "wanted.go", Patch: "@@ -1 +1 @@\n-x\n+y"}
		}
		return compareAnswer(files...), nil
	}}
	got, err := newWriteProvider(f).GetComparedFiles(context.Background(), "owner/repo", tBase, tHead, []string{"wanted.go"})
	if err != nil {
		t.Fatalf("GetComparedFiles: %v", err)
	}
	if len(got) != 1 || got[0].Path != "wanted.go" || len(f.calls) != 2 {
		t.Fatalf("files = %+v calls = %d", got, len(f.calls))
	}
	if !strings.Contains(strings.Join(f.calls[1], " "), "page=2") {
		t.Errorf("second call = %v", f.calls[1])
	}
}

// TestGetComparedFiles_TruncatedListingFailsClosed: every page is full and the
// wanted file is never seen, so its absence proves nothing.
func TestGetComparedFiles_TruncatedListingFailsClosed(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) {
		files := make([]compareFile, comparePageSize)
		for i := range files {
			files[i] = compareFile{Filename: fmt.Sprintf("f%d.go", i)}
		}
		return compareAnswer(files...), nil
	}}
	_, err := newWriteProvider(f).GetComparedFiles(context.Background(), "owner/repo", tBase, tHead, []string{"absent.go"})
	if !errors.Is(err, ErrCompareTruncated) {
		t.Fatalf("err = %v, want ErrCompareTruncated", err)
	}
	if len(f.calls) != maxComparePages {
		t.Errorf("calls = %d, want %d", len(f.calls), maxComparePages)
	}
}

// TestGetComparedFiles_ListingThatEndsMeansTheFileIsNotInTheDifference.
func TestGetComparedFiles_ListingThatEndsMeansTheFileIsNotInTheDifference(t *testing.T) {
	f := &writeFake{handle: func([]string, []byte) ([]byte, error) { return compareAnswer(compareFile{Filename: "a.go"}), nil }}
	got, err := newWriteProvider(f).GetComparedFiles(context.Background(), "owner/repo", tBase, tHead, []string{"absent.go"})
	if err != nil || len(got) != 0 {
		t.Fatalf("got = %+v err = %v", got, err)
	}
}

func TestGetComparedFiles_FailuresAndBadInput(t *testing.T) {
	p := newWriteProvider(&writeFake{handle: func([]string, []byte) ([]byte, error) { return nil, errors.New("gh: Not Found (HTTP 404)") }})
	if _, err := p.GetComparedFiles(context.Background(), "owner/repo", tBase, tHead, []string{"a.go"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("a host failure must be returned: %v", err)
	}
	bad := newWriteProvider(&writeFake{})
	for name, call := range map[string]func() error{
		"short head": func() error {
			_, e := bad.GetComparedFiles(context.Background(), "owner/repo", tBase, "abc1234", nil)
			return e
		},
		"base with a path in it": func() error {
			_, e := bad.GetComparedFiles(context.Background(), "owner/repo", "../../x", tHead, nil)
			return e
		},
		"repo": func() error {
			_, e := bad.GetComparedFiles(context.Background(), "norepo", tBase, tHead, nil)
			return e
		},
	} {
		if call() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	notJSON := newWriteProvider(&writeFake{handle: func([]string, []byte) ([]byte, error) { return []byte("[]"), nil }})
	if _, err := notJSON.GetComparedFiles(context.Background(), "owner/repo", tBase, tHead, []string{"a.go"}); err == nil {
		t.Error("a response that is not the compare shape must be an error")
	}
}

func TestIsFullSHA(t *testing.T) {
	for s, want := range map[string]bool{
		tHead:                   true,
		strings.ToUpper(tHead):  true,
		tHead[:39]:              false,
		tHead + "0":             false,
		"":                      false,
		strings.Repeat("g", 40): false,
	} {
		if IsFullSHA(s) != want {
			t.Errorf("IsFullSHA(%q) = %v, want %v", s, !want, want)
		}
	}
}
