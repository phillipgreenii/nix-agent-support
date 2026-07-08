package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func boolPtr(b bool) *bool { return &b }

// TestReviewEnabled_DefaultsTrue: an absent review section leaves reviews on,
// preserving today's behavior (the kill switch is opt-in).
func TestReviewEnabled_DefaultsTrue(t *testing.T) {
	if !(&Config{}).ReviewEnabled() {
		t.Error("absent review section must default to enabled=true")
	}
}

// TestReviewEnabled_NilReceiver: a nil *Config defaults to enabled.
func TestReviewEnabled_NilReceiver(t *testing.T) {
	var c *Config
	if !c.ReviewEnabled() {
		t.Error("nil *Config must default to enabled=true")
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
		"":                          true,  // no review section
		"review:\n  enabled: true":  true,  // explicit on
		"review:\n  enabled: false": false, // explicit off (the kill switch)
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
