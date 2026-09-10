// config_validate.go: the "pg-connector config validate" Tier-1-only CLI
// verb. It fans out both auth_status and capabilities across every
// registered backend through the same outcome-reporting envelope as
// "pg-connector auth status": one sources[] row per backend, marked
// succeeded only when both checks come back clean.
package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	"github.com/spf13/cobra"
)

// entityTypesWithList is the subset of entityTypes (registry.go) this
// packet's own "list" op spans — pr/issue only, per this packet's own
// Contract's two exemptions ("scm has no remote entity, and ci ... keeps
// its existing PR-keyed ci list <pr-id> verb unchanged"). Used by
// queryCoverageGaps below to scope which connector.<type> registrations
// query-name coverage is even meaningful for.
var entityTypesWithList = []string{"pr", "issue"}

// queryCoverageCheck computes whether query-name coverage is even a
// MEANINGFUL check for backend (applicable — true only when backend is
// registered under at least one entityTypesWithList type), and if so,
// the query names OTHER backends of that SAME type declare in their own
// backends.<name>.queries block that backend's own block does NOT define
// (gaps, bead pg2-2j5ac.28.1) — a config-authoring signal: a caller
// naming that query via --backend (or hitting a fan-out where every
// backend but this one happens to be down) would get
// query_not_recognized specifically from this backend.
//
// applicable is deliberately distinguished from "gaps == nil": a ci-only
// or scm-only backend (or ANY backend when nothing is registered under
// pr/issue at all — the common case in this file's own pre-existing unit
// tests, which pass a nil *Registry) is NOT applicable at all, and MUST
// NOT be folded into configValidateOne's count/degradation logic the same
// way a genuine zero-gap PASS would be — doing so would silently change
// what Count means for every backend this check has nothing to say about
// [bug discovered against this packet's own pre-existing
// TestFanOutConfigValidate_CountReflectsChecksPassed: an always-counted
// third check inflated Count from 2 to 3 even for backends with no
// pr/issue registration at all]. gaps is sorted and de-duplicated across
// every entityTypesWithList type backend is registered under (a
// multi-capability backend registered under both pr and issue would
// otherwise report the same gap twice).
func queryCoverageCheck(reg *Registry, backend string) (applicable bool, gaps []string, err error) {
	seen := map[string]bool{}
	for _, t := range entityTypesWithList {
		backends, listErr := reg.List(t)
		if listErr != nil {
			return false, nil, listErr
		}
		registered := false
		for _, b := range backends {
			if b == backend {
				registered = true
				break
			}
		}
		if !registered {
			continue
		}
		applicable = true
		union := map[string]bool{}
		var ownNames map[string]bool
		for _, b := range backends {
			names, namesErr := reg.BackendQueryNames(b)
			if namesErr != nil {
				return false, nil, namesErr
			}
			set := make(map[string]bool, len(names))
			for _, n := range names {
				union[n] = true
				set[n] = true
			}
			if b == backend {
				ownNames = set
			}
		}
		for n := range union {
			if !ownNames[n] {
				seen[n] = true
			}
		}
	}
	if !applicable {
		return false, nil, nil
	}
	if len(seen) == 0 {
		return true, nil, nil
	}
	gaps = make([]string, 0, len(seen))
	for n := range seen {
		gaps = append(gaps, n)
	}
	sort.Strings(gaps)
	return true, gaps, nil
}

// FanOutConfigValidate fans auth_status, capabilities, and (bead
// pg2-2j5ac.28.1) query-name coverage out across every backend in
// backends, building the sources[] envelope. Each backend gets exactly
// one row combining all three checks' verdict — never collapsed across
// backends, but the checks ARE combined per-backend since all exist to
// answer the single question "is this backend usable."
func FanOutConfigValidate(ctx context.Context, reg *Registry, backends []string) FanOutOutcome {
	// Sources starts as a non-nil empty slice so a zero-backend
	// (misconfigured host) result still marshals its sources[] field as
	// [] rather than null [bug A15].
	out := FanOutOutcome{Sources: make([]SourceResult, 0, len(backends))}
	for _, b := range backends {
		out.Sources = append(out.Sources, configValidateOne(ctx, reg, b))
	}
	return out
}

func configValidateOne(ctx context.Context, reg *Registry, backend string) SourceResult {
	var reasons []string

	authResult := authStatusOne(ctx, backend)
	authOK := authResult.Status == SourceSucceeded || authResult.Status == SourceDisabled
	if !authOK {
		reasons = append(reasons, "auth_status: "+authResult.Reason)
	}

	capsOK := true
	capsResp, err := scriptout.InvokeCapabilities(ctx, backend)
	if err == nil {
		// InvokeCapabilities already checked protocolVersion (see its own
		// doc comment); schemaVersion is per-capability, so it is checked
		// here against capsResp's own self-declared SchemaVersions map —
		// the payload this call used to discard entirely (bug pg2-p2z7o),
		// even though it is the one place a backend's schema version
		// actually travels (INV-VER-1).
		err = checkSchemaVersions(capsResp)
	}
	if err != nil {
		if !errors.Is(err, scriptout.ErrUnknownOp) {
			capsOK = false
			reasons = append(reasons, "capabilities: "+err.Error())
		}
	}

	// queryCoverage is applicable only when backend is registered under a
	// list-capable type (pr/issue) — see queryCoverageCheck's own doc
	// comment for why an inapplicable check must NOT be folded into
	// count/degradation the same way a genuine pass would be.
	applicable, gaps, gapErr := queryCoverageCheck(reg, backend)
	queriesOK := true
	if applicable {
		if gapErr != nil {
			queriesOK = false
			reasons = append(reasons, "query coverage: "+gapErr.Error())
		} else if len(gaps) > 0 {
			queriesOK = false
			reasons = append(reasons, fmt.Sprintf(
				"query coverage: missing %s (declared by another registered backend of the same type)",
				strings.Join(gaps, ", "),
			))
		}
	}

	// Count is the number of this source's checks that actually came back
	// healthy — its own raw pre-merge count (outcome.go's SourceResult doc
	// comment), rather than the hardcoded 0 that made a fully-degraded
	// backend indistinguishable from one that failed only one of the
	// checks [bug A16]. Query coverage only contributes to Count/the
	// overall verdict when applicable is true — see queryCoverageCheck's
	// own doc comment.
	count := 0
	if authOK {
		count++
	}
	if capsOK {
		count++
	}
	if applicable && queriesOK {
		count++
	}

	if authOK && capsOK && (!applicable || queriesOK) {
		return SourceResult{Source: backend, Status: SourceSucceeded, Count: count}
	}
	return SourceResult{Source: backend, Status: SourceDegraded, Count: count, Reason: strings.Join(reasons, "; ")}
}

// checkSchemaVersions compares resp's self-declared per-capability schema
// versions against schema.CurrentSchemaVersions (this build's own current
// expectations), returning a version_mismatch-wrapped error naming the
// first disagreement found, or nil if every capability resp declares
// matches (INV-VER-1). Keys are walked in sorted order so a genuine
// multi-capability mismatch always reports the same one first, rather than
// depending on Go's randomized map iteration order.
//
// A capability key resp declares that schema.CurrentSchemaVersions doesn't
// recognize (e.g. a future search-only backend built against a newer
// schema.CurrentSchemaVersions than this build's own) is skipped, not
// treated as a mismatch — this build simply has no opinion on a
// capability it doesn't itself know about. "attention" is no longer such
// an example capability as of pg2-2j5ac.15.1: it now has its own
// CurrentSchemaVersions entry, so a mismatch on it IS detected.
func checkSchemaVersions(resp *scriptout.CapabilitiesResponse) error {
	if resp == nil {
		return nil
	}
	names := make([]string, 0, len(resp.SchemaVersions))
	for name := range resp.SchemaVersions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		got := resp.SchemaVersions[name]
		want, known := schema.CurrentSchemaVersions[name]
		if !known {
			continue
		}
		if got != want {
			return scriptout.WrapError(scriptout.ErrVersionMismatch,
				fmt.Sprintf("capability %q schemaVersion %d != %d", name, got, want))
		}
	}
	return nil
}

func newConfigCmd() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Config-related commands",
	}
	configCmd.AddCommand(newConfigValidateCmd())
	configCmd.AddCommand(newConfigShowCmd())
	return configCmd
}

func newConfigValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Fan auth_status and capabilities out across every registered backend",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := LoadRegistry()
			if err != nil {
				return err
			}
			backends, err := reg.AllBackends()
			if err != nil {
				return err
			}
			outcome := FanOutConfigValidate(cmd.Context(), reg, backends)
			return writeFanOutResult(cmd, outcome, outcome.ExitCode(), func() string {
				return "config validate:\n" + formatSourcesTable(outcome.Sources)
			})
		},
	}
}
