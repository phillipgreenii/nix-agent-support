package scriptout

import (
	"context"
	"encoding/json"
	"testing"
)

func TestConfigFromContext_RoundTrip(t *testing.T) {
	cfg := json.RawMessage(`{"queries":{"team":"is:open"}}`)
	ctx := WithConfig(context.Background(), cfg)
	got := ConfigFromContext(ctx)
	if string(got) != string(cfg) {
		t.Fatalf("ConfigFromContext = %s, want %s", got, cfg)
	}
}

func TestConfigFromContext_NoneSet(t *testing.T) {
	if got := ConfigFromContext(context.Background()); got != nil {
		t.Fatalf("ConfigFromContext on a bare context = %q, want nil", got)
	}
}

func TestConfigFromContext_NilConfig(t *testing.T) {
	ctx := WithConfig(context.Background(), nil)
	if got := ConfigFromContext(ctx); got != nil {
		t.Fatalf("ConfigFromContext = %q, want nil", got)
	}
}
