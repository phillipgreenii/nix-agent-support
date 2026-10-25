package query

import (
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-issue-jira/internal/config"
	lq "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/query"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/schema"
)

func TestSettingsAnswerFromTheLoadedConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.Queries = map[string]schema.QueryDef{
		"zeta":  {Expr: schema.QueryExpr{"project = Z", "project = Y"}},
		"alpha": {Expr: schema.QueryExpr{"assignee = me"}, Watched: true},
	}
	cfg.QueryTTL = map[string]config.Duration{"alpha": config.Duration(7 * time.Second)}
	s := NewSettings(cfg)

	if e, ok := s.Expr("zeta"); !ok || len(e) != 2 || e[0] != "project = Z" || e[1] != "project = Y" {
		t.Errorf("Expr(zeta) = %v, %v", e, ok)
	}
	if _, ok := s.Expr("nope"); ok {
		t.Error("Expr(nope) = ok, want a name the config does not define to be unknown")
	}
	if !s.Watched("alpha") || s.Watched("zeta") || s.Watched("nope") {
		t.Errorf("Watched(alpha, zeta, nope) = %v, %v, %v, want true, false, false", s.Watched("alpha"), s.Watched("zeta"), s.Watched("nope"))
	}
	if n := s.Names(); len(n) != 2 || n[0] != "alpha" || n[1] != "zeta" {
		t.Errorf("Names() = %v, want [alpha zeta]", n)
	}
	if got := s.TTL(lq.Key{Name: "alpha"}); got != 7*time.Second {
		t.Errorf("TTL(alpha) = %s, want the query_ttl entry", got)
	}
	if got := s.TTL(lq.Key{Name: "zeta"}); got != config.DefaultQueryTTLDefault {
		t.Errorf("TTL(zeta) = %s, want query_ttl_default", got)
	}
	if got := s.TTL(lq.Key{Name: "alpha", Args: map[string]string{"k": "v"}}); got != config.DefaultQueryTTLDefault {
		t.Errorf("TTL(ad-hoc alpha) = %s, want the ad-hoc ttl (query_ttl_default)", got)
	}
}
