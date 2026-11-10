//go:build contract

// contract_test.go: the on-demand contract suite for pg-connector-alert-pagerduty.
// It calls the REAL PagerDuty REST API with the operator's own API key, to
// prove the assumptions the unit tests can only fake: the key resolves and is
// accepted, a rejected key is reported as unauthenticated, the incident and
// alert JSON still has the shape client.go decodes, PagerDuty's alert-severity
// vocabulary is still the four values alertSeverity maps, and the Backend's
// answers agree with the raw API.
//
// It is NOT a unit test and NOT a flake check: it needs the macOS Keychain
// item (service pd-schedule-manager, account pagerduty-api, or the
// PAGERDUTY_API_TOKEN environment variable) and outbound network, neither of
// which a build sandbox has. It runs only when the operator starts it:
//
//	nix run .#pg-connector-contract                  # the whole contract tier
//	cd packages/pg-connector && go test -tags contract -count=1 -v \
//	    ./cmd/pg-connector-alert-pagerduty/...       # just this suite
//
// The checks.<system>.pg-connector-integration-tests-gating check vets this
// file (`go vet -tags contract`), so it cannot rot at compile level unseen.
//
// Every call is a read (GET). A missing key FAILS the test loudly, naming where
// the key is looked up, rather than skipping quietly: this tier exists to be
// run deliberately, and a silent skip would read as a pass.
package internal

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// documentedAlertSeverities is PagerDuty's alert-severity vocabulary (Events API
// v2) that alertSeverity maps. A value outside it means the closed mapping table
// is missing a case.
var documentedAlertSeverities = map[string]bool{"critical": true, "error": true, "warning": true, "info": true}

// sampleLimit bounds how many incidents the severity tests ask the alerts
// endpoint about, so a run stays a few dozen read calls.
const sampleLimit = 40

func liveContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return scriptout.WithConfig(ctx, []byte(`{}`))
}

// liveToken resolves the real key exactly as the backend does and fails the
// test when there is none.
func liveToken(t *testing.T, ctx context.Context) string {
	t.Helper()
	cfg, err := decodeConfig(ctx)
	if err != nil {
		t.Fatalf("decode default config: %v", err)
	}
	tok, err := NewTokenResolver(os.Getenv).Resolve(ctx, cfg)
	if err != nil {
		t.Fatalf("no PagerDuty API key for the contract tests: %v (looked in Keychain service %q account %q, then $%s)",
			err, cfg.KeychainService, cfg.KeychainAccount, TokenEnvVar)
	}
	return tok
}

// firingQuery is the backend's own firing-only filter.
const firingQuery = "statuses[]=triggered&statuses[]=acknowledged"

// recentResolvedQuery adds a bounded window of resolved incidents so the
// vocabulary check has data on a quiet day.
func recentResolvedQuery() string {
	since := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339)
	return "statuses[]=resolved&since=" + url.QueryEscape(since)
}

// sampleIncidents returns up to sampleLimit incidents: every firing one first,
// then recently resolved ones.
func sampleIncidents(t *testing.T, ctx context.Context, tok string) []apiIncident {
	t.Helper()
	c := NewHTTPClient()
	out, err := c.Incidents(ctx, DefaultBaseURL, tok, firingQuery)
	if err != nil {
		t.Fatalf("list firing incidents: %v", err)
	}
	if len(out) < sampleLimit {
		more, err := c.Incidents(ctx, DefaultBaseURL, tok, recentResolvedQuery())
		if err != nil {
			t.Fatalf("list recently resolved incidents: %v", err)
		}
		out = append(out, more...)
	}
	if len(out) > sampleLimit {
		out = out[:sampleLimit]
	}
	return out
}

func TestPagerDutyContract_KeyResolvesAndIsAccepted(t *testing.T) {
	ctx := liveContext(t)
	b := New(NewHTTPClient(), NewTokenResolver(os.Getenv))
	if err := b.CheckAuth(ctx); err != nil {
		t.Fatalf("CheckAuth: %v", err)
	}
	// CheckAuth makes no network call; this proves PagerDuty accepts the key.
	if _, err := NewHTTPClient().Incidents(ctx, DefaultBaseURL, liveToken(t, ctx), firingQuery); err != nil {
		t.Fatalf("PagerDuty did not accept the resolved key: %v", err)
	}
}

func TestPagerDutyContract_RejectedKeyIsUnauthorized(t *testing.T) {
	ctx := liveContext(t)
	_, err := NewHTTPClient().Incidents(ctx, DefaultBaseURL, "not-a-real-key", firingQuery)
	if !errors.Is(err, errUnauthorized) {
		t.Fatalf("a bogus key gave %v, want errUnauthorized (a 401/403 mapped by client.go)", err)
	}
}

func TestPagerDutyContract_IncidentJSONShape(t *testing.T) {
	ctx := liveContext(t)
	incs := sampleIncidents(t, ctx, liveToken(t, ctx))
	if len(incs) == 0 {
		t.Fatal("no incidents to inspect: neither a firing nor a resolved one in the last 30 days")
	}
	for _, in := range incs {
		if in.ID == "" || in.HTMLURL == "" {
			t.Errorf("incident %+v lacks id or html_url", in)
		}
		if _, err := time.Parse(time.RFC3339Nano, in.CreatedAt); err != nil {
			t.Errorf("incident %s: created_at %q does not parse: %v", in.ID, in.CreatedAt, err)
		}
		if in.Urgency != "high" && in.Urgency != "low" {
			t.Errorf("incident %s: urgency %q is not high or low, so urgencySeverity falls back to nothing", in.ID, in.Urgency)
		}
		if in.Service.ID == "" {
			t.Errorf("incident %s has no service id", in.ID)
		}
	}
}

// TestPagerDutyContract_AlertSeverityVocabulary is the live check the design's
// "PagerDuty severity vocabulary" item asks for: every severity PagerDuty sends
// on an incident's alerts is one of the four values alertSeverity maps.
func TestPagerDutyContract_AlertSeverityVocabulary(t *testing.T) {
	ctx := liveContext(t)
	tok := liveToken(t, ctx)
	c := NewHTTPClient()
	observed := map[string]int{}
	var unknown []string
	for _, in := range sampleIncidents(t, ctx, tok) {
		alerts, err := c.IncidentAlerts(ctx, DefaultBaseURL, tok, in.ID)
		if err != nil {
			t.Fatalf("alerts of incident %s: %v", in.ID, err)
		}
		for _, a := range alerts {
			v := strings.ToLower(strings.TrimSpace(a.Severity))
			observed[v]++
			if !documentedAlertSeverities[v] {
				unknown = append(unknown, in.ID+"="+a.Severity)
			}
		}
	}
	if len(observed) == 0 {
		t.Fatal("the sampled incidents carry no alerts, so the vocabulary was not exercised")
	}
	keys := make([]string, 0, len(observed))
	for k := range observed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("alert severity %q: %d alert(s)", k, observed[k])
	}
	if len(unknown) > 0 {
		t.Fatalf("severities outside critical/error/warning/info (alertSeverity maps them to nothing): %v", unknown)
	}
}

// TestPagerDutyContract_BackendAgreesWithRawAPI checks the Backend's mapped
// severity against the raw alert severities it reports, and that List and Show
// agree.
func TestPagerDutyContract_BackendAgreesWithRawAPI(t *testing.T) {
	ctx := liveContext(t)
	b := New(NewHTTPClient(), NewTokenResolver(os.Getenv))
	res, err := b.List(ctx, nil, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Entities) == 0 {
		t.Skip("nothing is firing in PagerDuty right now; the agreement check needs at least one firing incident")
	}
	for _, a := range res.Entities {
		var ext pagerdutyExtension
		if err := json.Unmarshal(a.Extensions["pagerduty"], &ext); err != nil {
			t.Fatalf("%s: extensions.pagerduty: %v", a.ID, err)
		}
		if got, want := a.Severity, wantSeverity(ext); got != want {
			t.Errorf("%s: severity %q, but alert_severities %v and urgency %q imply %q", a.ID, got, ext.AlertSeverities, ext.Urgency, want)
		}
		if a.Provider != providerName || !strings.HasPrefix(a.ID, idPrefix) {
			t.Errorf("%s: provider %q or id prefix wrong", a.ID, a.Provider)
		}
	}
	first := res.Entities[0]
	shown, err := b.Show(ctx, first.ID)
	if err != nil {
		t.Fatalf("Show(%s): %v", first.ID, err)
	}
	if shown.Severity != first.Severity || shown.Title != first.Title {
		t.Errorf("Show(%s) = severity %q title %q, List said severity %q title %q", first.ID, shown.Severity, shown.Title, first.Severity, first.Title)
	}
}

// wantSeverity recomputes the severity the design specifies from the raw data:
// the highest recognized alert severity, else the urgency fallback.
func wantSeverity(ext pagerdutyExtension) schema.Severity {
	var best schema.Severity
	for _, raw := range ext.AlertSeverities {
		if s := alertSeverity(raw); severityRank(s) > severityRank(best) {
			best = s
		}
	}
	if best == "" {
		best = urgencySeverity(ext.Urgency)
	}
	return best
}

func TestPagerDutyContract_ShowOfAnUnknownIncidentIsNotFound(t *testing.T) {
	ctx := liveContext(t)
	b := New(NewHTTPClient(), NewTokenResolver(os.Getenv))
	_, err := b.Show(ctx, idPrefix+"PZZZZZZ")
	if !errors.Is(err, scriptout.ErrNotFound) {
		t.Fatalf("Show of an id PagerDuty does not have gave %v, want not_found", err)
	}
}
