package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func boolPtr(b bool) *bool { return &b }

// TestReviewEnabled_DefaultsFalse: an absent review section leaves the pg-pr
// review hook OFF, so the repo's built-in default is a single-owner resting
// state (pr-pool owns reviews) rather than double-writing a shared bead store
// alongside the pg-pr hook (pg2-3ho1r). Enabling the legacy pg-pr review path is
// strictly opt-in via review.enabled=true.
func TestReviewEnabled_DefaultsFalse(t *testing.T) {
	if (&Config{}).ReviewEnabled() {
		t.Error("absent review section must default to enabled=false (resting-safe single owner)")
	}
}

// TestReviewEnabled_NilReceiver: a nil *Config defaults to disabled.
func TestReviewEnabled_NilReceiver(t *testing.T) {
	var c *Config
	if c.ReviewEnabled() {
		t.Error("nil *Config must default to enabled=false (resting-safe single owner)")
	}
}

func TestReviewEnabled_ExplicitFalse(t *testing.T) {
	c := &Config{Review: ReviewConfig{Enabled: boolPtr(false)}}
	if c.ReviewEnabled() {
		t.Error("review.enabled=false must disable reviews")
	}
}

func TestReviewEnabled_ExplicitTrue(t *testing.T) {
	c := &Config{Review: ReviewConfig{Enabled: boolPtr(true)}}
	if !c.ReviewEnabled() {
		t.Error("review.enabled=true must enable reviews")
	}
}

// TestReviewEnabled_YAML confirms the yaml key path is exactly review.enabled.
func TestReviewEnabled_YAML(t *testing.T) {
	cases := map[string]bool{
		"":                          false, // no review section → resting-safe off (pg2-3ho1r)
		"review:\n  enabled: true":  true,  // explicit opt-in
		"review:\n  enabled: false": false, // explicit off
	}
	for src, want := range cases {
		var c Config
		if err := yaml.Unmarshal([]byte(src), &c); err != nil {
			t.Fatalf("unmarshal %q: %v", src, err)
		}
		if got := c.ReviewEnabled(); got != want {
			t.Errorf("ReviewEnabled() for %q = %v, want %v", src, got, want)
		}
	}
}
