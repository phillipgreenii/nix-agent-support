package collector

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
)

// Envelope is the pg-desk.changes/v1 output of `pg-desk pr changes --json`.
type Envelope struct {
	Contract string `json:"contract"`
	Type     string `json:"type"`
	Consumer string `json:"consumer"`
	Cursor   struct {
		From int64 `json:"from"`
		To   int64 `json:"to"`
	} `json:"cursor"`
	Sources []schema.Source `json:"sources"`
	Records []struct {
		Seq     int64    `json:"seq"`
		Type    string   `json:"type"`
		ID      string   `json:"id"`
		Version int64    `json:"version"`
		Kinds   []string `json:"kinds"`
		Origin  string   `json:"origin"`
		At      string   `json:"at"`
	} `json:"records"`
}

// ParseEnvelope decodes the envelope. Exit 3 (total failure) still carries one.
func ParseEnvelope(stdout string) (Envelope, error) {
	var env Envelope
	if strings.TrimSpace(stdout) == "" {
		return env, fmt.Errorf("empty output")
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		return env, fmt.Errorf("decode envelope: %w", err)
	}
	return env, nil
}

var (
	refRE   = regexp.MustCompile(`[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(#[0-9]+)?`)
	atRE    = regexp.MustCompile(`@[A-Za-z0-9_-]+`)
	pathRE  = regexp.MustCompile(`/[A-Za-z0-9_.@+-]+(/[A-Za-z0-9_.@+-]+)+`)
	spaceRE = regexp.MustCompile(`\s+`)
)

// Scrub makes an error text safe to leave the scratch directory: known
// literals (login, repo slug) and every owner/repo, #number, @handle and path
// shaped token are replaced, whitespace collapsed and the text truncated.
func Scrub(s string, literals []string) string {
	s = pathRE.ReplaceAllString(s, "<path>")
	for _, l := range literals {
		if l != "" {
			s = strings.ReplaceAll(s, l, "<redacted>")
		}
	}
	s = refRE.ReplaceAllString(s, "<ref>")
	s = atRE.ReplaceAllString(s, "<@>")
	s = spaceRE.ReplaceAllString(strings.TrimSpace(s), " ")
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}

func parseKinds(raw string) []string {
	var out []string
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}
