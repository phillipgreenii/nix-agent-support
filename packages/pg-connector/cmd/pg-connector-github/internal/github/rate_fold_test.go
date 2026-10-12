package github

import "testing"

// FoldedRateLimit reads the selection this package's own documents carry
// (rateLimitSelection), so for the same response body it MUST agree with the
// reading the gated reads hand their gate: one definition of the wire shape,
// not a second parser (the budget governor's feed takes its GraphQL readings
// from it).
func TestFoldedRateLimit_AgreesWithTheReadingTheGateSees(t *testing.T) {
	for _, g := range gatedReads {
		t.Run(g.name, func(t *testing.T) {
			body := readingPage(g.name, 3987, false, "")
			cost, remaining, resetAt, found, err := FoldedRateLimit(body)
			if err != nil || !found {
				t.Fatalf("FoldedRateLimit: found=%v err=%v", found, err)
			}
			if cost != 1 || remaining != 3987 || resetAt != "2026-10-08T01:00:00Z" {
				t.Errorf("cost %d remaining %d resetAt %q", cost, remaining, resetAt)
			}

			var seen []RateLimit
			if err := g.run(NewWithRunner(&sequencedGH{responses: [][]byte{body}}), func(rl RateLimit) error {
				seen = append(seen, rl)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if len(seen) != 1 || seen[0].Remaining != remaining || seen[0].ResetAt != resetAt {
				t.Errorf("gate saw %+v, FoldedRateLimit read remaining %d resetAt %q", seen, remaining, resetAt)
			}
		})
	}
}

func TestFoldedRateLimit_AbsentAndMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"a document that did not select it": `{"data":{"viewer":{"login":"example-user"}}}`,
		"null data":                         `{"data":null,"errors":[{"message":"boom"}]}`,
		"an empty object":                   `{}`,
	} {
		if _, _, _, found, err := FoldedRateLimit([]byte(body)); found || err != nil {
			t.Errorf("%s: found=%v err=%v, want neither", name, found, err)
		}
	}
	if _, _, _, _, err := FoldedRateLimit([]byte("HTTP/2.0 502 Bad Gateway")); err == nil {
		t.Error("a body that is not JSON was accepted")
	}
}
