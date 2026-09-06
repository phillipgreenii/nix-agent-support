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

// FanOutConfigValidate fans both auth_status and capabilities out across
// every backend in backends, building the sources[] envelope. Each backend
// gets exactly one row combining both checks' verdict — never collapsed
// across backends, but the two checks ARE combined per-backend since both
// exist to answer the single question "is this backend usable."
func FanOutConfigValidate(ctx context.Context, backends []string) FanOutOutcome {
	// Sources starts as a non-nil empty slice so a zero-backend
	// (misconfigured host) result still marshals its sources[] field as
	// [] rather than null [bug A15].
	out := FanOutOutcome{Sources: make([]SourceResult, 0, len(backends))}
	for _, b := range backends {
		out.Sources = append(out.Sources, configValidateOne(ctx, b))
	}
	return out
}

func configValidateOne(ctx context.Context, backend string) SourceResult {
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
		// actually travels [design: §4.3].
		err = checkSchemaVersions(capsResp)
	}
	if err != nil {
		if !errors.Is(err, scriptout.ErrUnknownOp) {
			capsOK = false
			reasons = append(reasons, "capabilities: "+err.Error())
		}
	}

	if authOK && capsOK {
		return SourceResult{Source: backend, Status: SourceSucceeded, Count: 0}
	}
	return SourceResult{Source: backend, Status: SourceDegraded, Count: 0, Reason: strings.Join(reasons, "; ")}
}

// checkSchemaVersions compares resp's self-declared per-capability schema
// versions against schema.CurrentSchemaVersions (this build's own current
// expectations), returning a version_mismatch-wrapped error naming the
// first disagreement found, or nil if every capability resp declares
// matches [design: §4.3]. Keys are walked in sorted order so a genuine
// multi-capability mismatch always reports the same one first, rather than
// depending on Go's randomized map iteration order.
//
// A capability key resp declares that schema.CurrentSchemaVersions doesn't
// recognize (e.g. a future attention/search-only backend this build
// doesn't yet know about) is skipped, not treated as a mismatch — this
// build simply has no opinion on a capability it doesn't itself know
// about.
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
			outcome := FanOutConfigValidate(cmd.Context(), backends)
			return writeFanOutResult(cmd, outcome, outcome.ExitCode(), func() string {
				return "config validate:\n" + formatSourcesTable(outcome.Sources)
			})
		},
	}
}
