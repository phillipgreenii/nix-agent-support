package config

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBlockStamp(t *testing.T) {
	for name, tc := range map[string]struct {
		block      string
		hash, path string
		wantErr    bool
	}{
		"empty":      {block: ``},
		"null":       {block: `null`},
		"no stamp":   {block: `{"other": 1}`},
		"stamped":    {block: `{"config_hash": "h", "config_path": "/p"}`, hash: "h", path: "/p"},
		"non-object": {block: `[1, 2]`},
		"malformed":  {block: `{"config_hash"`, wantErr: true},
	} {
		h, p, err := BlockStamp(json.RawMessage(tc.block))
		if (err != nil) != tc.wantErr || h != tc.hash || p != tc.path {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, err=%v)", name, h, p, err, tc.hash, tc.path, tc.wantErr)
		}
	}
}

func TestCheckBlock(t *testing.T) {
	cfg, err := Load(write(t, `{"state_dir": "/tmp/s", "pjira_path": "/p", "config_hash": "good"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{``, `null`, `{}`, `{"unrelated": true}`, `{"config_hash": "good"}`, `{"config_hash": "good", "config_path": "/x"}`} {
		if err := CheckBlock(cfg, json.RawMessage(ok)); err != nil {
			t.Errorf("block %q must be accepted: %v", ok, err)
		}
	}
	err = CheckBlock(cfg, json.RawMessage(`{"config_hash": "bad", "config_path": "/etc/other.json"}`))
	if !errors.Is(err, ErrConfigHashMismatch) {
		t.Fatalf("a differing hash must be rejected, got %v", err)
	}
	for _, want := range []string{"bad", "good", "/etc/other.json", cfg.Path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	err = CheckBlock(cfg, json.RawMessage(`{"config_hash": "bad"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("a block without a path is named unknown: %v", err)
	}
}
