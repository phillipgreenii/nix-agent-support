// pg-desk-attention is pg-desk's attention plugin: a standalone
// `list_attention` backend, registered by bare name in the connector
// umbrella's `attention.sources`, that answers "which entities need the
// operator now" from pg-desk's local store (docs/behavior/pg-desk/attention.md,
// "The plugin"; ADR 0081).
//
// It speaks only the script-out wire protocol (pkg/scriptout.ServeLoop: one
// request on stdin, one response on stdout) and answers `list_attention` and
// `capabilities`. Every decision is made by attention.Evaluate, the same
// pure function the dashboard payload and `pg-desk attention list` call
// (INV-ATTNEVAL-2); this binary only translates its result onto the wire.
//
// It execs nothing and opens no network connection: it reads the local
// store through a read-only handle (cmd/pg-desk's own `attention list`
// seam, duplicated here because that one lives in package main of another
// command). pg-desk's composition rule (docs/behavior/pg-desk/README.md) is
// therefore unchanged by it. main_test.go enforces that with a source scan
// and a tripwire run.
//
// Dependency direction (ADR 0077): this binary lives in pg-desk and imports
// pg-connector's wire packages; pg-connector knows it only as a bare name in
// a registry, never by import.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/provider/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
	deskattention "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/attention"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/interpret"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Version is stamped at build time (mkGoApp's default versionPath).
var Version = "dev"

func main() {
	os.Exit(run())
}

// run builds the production provider and serves exactly one request.
func run() int {
	return scriptout.ServeLoop(newDispatchTable(&provider{
		loadConfig: config.Load,
		openStore:  func() (*store.Store, error) { return store.OpenReadOnly(store.DefaultPath()) },
		clock:      interpret.SystemClock{},
	}))
}

// newDispatchTable builds the attention capability's table from p and adds
// the capabilities entry. capabilities.ops is computed from the table's own
// registered keys by scriptout.AddCapabilities, so this binary never
// hand-types an ops list that could drift (it never claims auth_status:
// provider does not implement provider.AuthChecker, and there is nothing to
// authenticate against).
func newDispatchTable(p *provider) scriptout.DispatchTable {
	return scriptout.AddCapabilities(attention.NewDispatchTable(p), schema.AttentionSchemaVersion, scriptout.CapabilitiesResponse{
		ProtocolVersion: scriptout.ProtocolVersion,
		SchemaVersions:  map[string]int{"attention": schema.AttentionSchemaVersion},
		Version:         Version,
	})
}

// provider implements pkg/provider/attention.Provider over pg-desk's local
// store. The three fields are seams so tests inject a fixture config, store
// and clock; production wires them in run.
type provider struct {
	loadConfig func(context.Context) (*config.Config, error)
	openStore  func() (*store.Store, error)
	clock      deskattention.Clock
}

var _ attention.Provider = (*provider)(nil)

// ListAttention evaluates attention over the local store and returns one item
// per entity that needs the operator, with the entity type as the item type,
// in the evaluator's canonical order (the umbrella's stable severity sort keeps
// that order among equal ranks). A configuration or store that cannot be read
// is an error, never an empty list that would read as "all clear"
// (INV-ATTNEVAL-6): the umbrella then reports this source degraded.
func (p *provider) ListAttention(ctx context.Context) ([]schema.AttentionItem, error) {
	cfg, err := p.loadConfig(ctx)
	if err != nil {
		return nil, unavailable("load config: %v", err)
	}
	if len(cfg.Repos) == 0 {
		return nil, unavailable("no repository configured")
	}
	st, err := p.openStore()
	if err != nil {
		return nil, unavailable("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	res, err := deskattention.Evaluate(deskattention.Inputs{Store: st, Repo: cfg.Repos[0].Remote, Config: cfg, Clock: p.clock})
	if err != nil {
		return nil, unavailable("%v", err)
	}
	return toWire(res), nil
}

// toWire maps the evaluator's items, in order, onto the wire shape. The group
// label comes from the group the item belongs to; an item whose group has no
// label falls back to the key, so a group is never emitted without one.
func toWire(res deskattention.Result) []schema.AttentionItem {
	labels := make(map[string]string, len(res.Groups))
	for _, g := range res.Groups {
		labels[g.Key] = g.Label
	}
	out := make([]schema.AttentionItem, 0, len(res.Items))
	for _, it := range res.Items {
		item := schema.AttentionItem{
			Type:     it.Type,
			ID:       it.ID,
			Summary:  it.Summary,
			Severity: schema.Severity(it.Severity),
		}
		if it.Group != "" {
			label := labels[it.Group]
			if label == "" {
				label = it.Group
			}
			item.Group = &schema.AttentionGroup{Key: it.Group, Label: label}
		}
		out = append(out, item)
	}
	return out
}

func unavailable(format string, args ...any) error {
	return scriptout.WrapError(scriptout.ErrUnavailable, "pg-desk-attention: "+fmt.Sprintf(format, args...))
}
