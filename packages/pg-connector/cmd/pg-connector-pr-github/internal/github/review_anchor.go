package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// This file holds the READS review_submit needs to save a review at an
// earlier head of the PR (INV-REVHEAD-1..3): the PR's commit list with its
// base tip, and the per-file patches of the difference between that base tip
// and the earlier head. Like review_write.go it lives apart from github.go on
// purpose (github.go is hash-pinned against pg-pr's copy, and none of this
// exists in pg-pr).

// fullSHARE matches a full 40-character commit sha.
var fullSHARE = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// IsFullSHA reports whether s is a full 40-character hexadecimal commit sha.
func IsFullSHA(s string) bool { return fullSHARE.MatchString(s) }

// PRHistory is the commit list of a PR together with its base tip.
type PRHistory struct {
	// BaseOID is the CURRENT tip of the PR's base branch.
	BaseOID string
	// CommitSHAs are the PR's commits as the host lists them (at most
	// maxCommits of them), in the host's order.
	CommitSHAs []string
	// Truncated is true when the host reports more commits than were read, so
	// a sha missing from CommitSHAs may still be a commit of the PR.
	Truncated bool
}

// prHistoryPageQuery reads one page of a PR's commit shas plus the base tip.
// It selects rateLimit { cost } like the other documents this connector owns.
const prHistoryPageQuery = `
query($owner: String!, $name: String!, $number: Int!, $after: String) {
  rateLimit { cost }
  repository(owner: $owner, name: $name) {
    pullRequest(number: $number) {
      baseRefOid
      commits(first: 100, after: $after) {
        totalCount
        pageInfo { hasNextPage endCursor }
        nodes { commit { oid } }
      }
    }
  }
}
`

type prHistoryNode struct {
	Commit struct {
		OID string `json:"oid"`
	} `json:"commit"`
}

type prHistoryPage struct {
	BaseRefOid string                  `json:"baseRefOid"`
	Commits    connPage[prHistoryNode] `json:"commits"`
}

// GetPRHistory reads the PR's commit shas (at most maxCommits) and the current
// tip of its base branch. A list the host reports as longer than what was read
// is marked Truncated, never silently cut.
func (p *Provider) GetPRHistory(ctx context.Context, repo string, number int) (*PRHistory, error) {
	owner, name, err := splitRepo(repo, number)
	if err != nil {
		return nil, err
	}
	vars := map[string]string{"owner": owner, "name": name}
	base := ""
	nodes, total, err := fetchConnection(maxCommits, "", func(after string) (connPage[prHistoryNode], error) {
		var d prQueryEnvelope[prHistoryPage]
		if err := p.runGraphQL(ctx, prHistoryPageQuery, vars, number, after, &d); err != nil {
			return connPage[prHistoryNode]{}, err
		}
		if d.Repository.PullRequest == nil {
			return connPage[prHistoryNode]{}, errPRNotResolved(repo, number)
		}
		if base == "" {
			base = d.Repository.PullRequest.BaseRefOid
		}
		return d.Repository.PullRequest.Commits, nil
	})
	if err != nil {
		return nil, err
	}
	out := &PRHistory{BaseOID: base, CommitSHAs: make([]string, 0, len(nodes)), Truncated: total > len(nodes)}
	for _, n := range nodes {
		out.CommitSHAs = append(out.CommitSHAs, n.Commit.OID)
	}
	return out, nil
}

// ComparedFile is one file of the difference between two commits.
type ComparedFile struct {
	// Path is the file's path at the later commit; PreviousPath is its path at
	// the earlier one when the file was renamed ("" otherwise).
	Path         string
	PreviousPath string
	// Patch is the file's diff text from its first "@@" line on, "" when the
	// host omitted it (a large patch, a binary file, a rename without changes).
	Patch string
}

// ErrCompareTruncated is returned by GetComparedFiles when the listing ended
// at its page cap before every wanted path was found, so a missing file may
// still be part of the difference.
var ErrCompareTruncated = errors.New("github: the file list of the comparison was cut at its page cap before every wanted file was found")

const (
	// comparePageSize is the most files one compare page returns.
	comparePageSize = 300
	// maxComparePages bounds the paging at maxFiles files.
	maxComparePages = maxFiles / comparePageSize
)

type compareResponse struct {
	Files []struct {
		Filename         string `json:"filename"`
		PreviousFilename string `json:"previous_filename"`
		Patch            string `json:"patch"`
	} `json:"files"`
}

// GetComparedFiles reads the files of the difference between base and head
// (REST compare, three dots: head against its merge base with base), paging
// until every wanted path has been found or the listing ends. A wanted path
// matches a file by its path at head or by its previous path (a rename). Only
// the matching files are returned; a wanted path that is not among them is not
// part of the difference, unless the call fails with ErrCompareTruncated.
// base and head MUST be full commit shas.
func (p *Provider) GetComparedFiles(ctx context.Context, repo, base, head string, wanted []string) ([]ComparedFile, error) {
	if err := validateRepo(repo); err != nil {
		return nil, err
	}
	if !IsFullSHA(base) || !IsFullSHA(head) {
		return nil, fmt.Errorf("github: compare needs full commit shas, got %q...%q", base, head)
	}
	want := make(map[string]bool, len(wanted))
	for _, w := range wanted {
		want[w] = true
	}
	var out []ComparedFile
	found := map[string]bool{}
	for page := 1; page <= maxComparePages; page++ {
		raw, err := p.runRead(ctx, readOpts{}, "api",
			fmt.Sprintf("repos/%s/compare/%s...%s?per_page=%d&page=%d", repo, base, head, comparePageSize, page))
		if err != nil {
			return nil, fmt.Errorf("github: compare %s...%s: %w", base[:7], head[:7], err)
		}
		var resp compareResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, fmt.Errorf("github: parse compare response: %w", err)
		}
		for _, f := range resp.Files {
			hit := false
			for _, key := range []string{f.Filename, f.PreviousFilename} {
				if key != "" && want[key] {
					found[key] = true
					hit = true
				}
			}
			if hit {
				out = append(out, ComparedFile{Path: f.Filename, PreviousPath: f.PreviousFilename, Patch: f.Patch})
			}
		}
		if len(found) == len(want) || len(resp.Files) < comparePageSize {
			return out, nil
		}
	}
	return out, fmt.Errorf("%w (%d pages of %d files)", ErrCompareTruncated, maxComparePages, comparePageSize)
}

// ResolveAnchor finds where a new point (path, side, line) sits in the
// compared files, as the diff position the host's position-based append
// takes, together with the path to send. RIGHT resolves line against new-file
// numbering under the file's path at head; LEFT resolves it against old-file
// numbering under the file's path at head or, for a renamed file, under its
// previous path (the path to send is then the file's path at head, the name
// the diff is listed under). ok is false when the file is not in the
// difference, its patch is unavailable, or the line is not in the diff.
func ResolveAnchor(files []ComparedFile, path, side string, line int) (sendPath string, position int, ok bool) {
	for _, f := range files {
		if f.Path != path && !(strings.EqualFold(side, "LEFT") && f.PreviousPath == path) {
			continue
		}
		if f.Patch == "" {
			return "", 0, false
		}
		pos, found := ParsePatchPositions(f.Patch).Position(side, line)
		if !found {
			return "", 0, false
		}
		return f.Path, pos, true
	}
	return "", 0, false
}
