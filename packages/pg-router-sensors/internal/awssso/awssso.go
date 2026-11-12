// Package awssso implements aws-sso-sensor: the resource aws_sso_zr is
// AVAILABLE while the AWS CLI v2 SSO cache holds a usable login for the
// configured SSO session name.
//
// The AWS CLI keeps one JSON file per login in the cache directory (default
// $HOME/.aws/sso/cache), named by the sha1 of the session name (or of the
// start URL for a legacy login), with the fields startUrl, sessionName (not
// always present), region, accessToken, expiresAt, clientId, clientSecret,
// registrationExpiresAt and refreshToken. A session is usable when the access
// token is unexpired, or when a refresh token is present and the client
// registration (registrationExpiresAt, when present) is still in the future,
// because the CLI then renews the access token itself.
//
// SECURITY: the sensor reads only the expiry fields, the two identity fields
// used for matching, and the PRESENCE of a refresh token. Token and client
// secret values are never decoded into any variable, so they cannot be
// printed, logged or returned. A missing cache directory, no matching file and
// a malformed file are all ordinary UNAVAILABLE answers or skipped files, never
// errors (the unknown value of a credential is the worst level).
//
// Reading: seconds until the session can no longer be renewed (unit s_left):
// registrationExpiresAt when a refresh token is present and the field exists,
// else the access token's expiresAt. With a refresh token and no
// registrationExpiresAt the horizon is unknown, so the session counts as
// usable, the reading is the access token's remaining seconds (never negative)
// and the margin is not applied.
//
// Config keys: sso_session (default "zip"), cache_dir (default
// $HOME/.aws/sso/cache), start_url (optional extra match on startUrl), margin
// (duration, default 30m).
//
// Exported names: Sensor, New, Resource, DefaultSession, DefaultMargin.
package awssso

import (
	"context"
	"crypto/sha1" //nolint:gosec // the AWS CLI names its cache files by sha1; not a security use
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

// Resource is the one resource this sensor answers.
const Resource = "aws_sso_zr"

// Defaults.
const (
	DefaultSession = "zip"
	DefaultMargin  = 30 * time.Minute
	reasonUnit     = "s_left"
)

// present is true when a JSON value is a non-empty string; the value itself is
// discarded during decoding, never stored.
type present bool

// UnmarshalJSON implements json.Unmarshaler.
func (p *present) UnmarshalJSON(b []byte) error {
	*p = len(b) > 2 && b[0] == '"'
	return nil
}

// cacheFile is the only view of a cache file the sensor takes: no accessToken,
// clientSecret or refreshToken value is decoded.
type cacheFile struct {
	StartURL       string  `json:"startUrl"`
	SessionName    string  `json:"sessionName"`
	ExpiresAt      string  `json:"expiresAt"`
	RegistrationAt string  `json:"registrationExpiresAt"`
	RefreshToken   present `json:"refreshToken"`
	AccessToken    present `json:"accessToken"`
}

// parseTime reads the timestamp forms the AWS CLI writes.
func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05UTC", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Sensor is the aws-sso-sensor probe set.
type Sensor struct {
	Session  string
	CacheDir string // "" means $HOME/.aws/sso/cache
	StartURL string
	Margin   time.Duration
}

// New returns a Sensor with the defaults.
func New() *Sensor { return &Sensor{Session: DefaultSession, Margin: DefaultMargin} }

// Resources implements sensorcli.Sensor.
func (s *Sensor) Resources() []string { return []string{Resource} }

// Bands implements sensorcli.Sensor: a two-level scale has no bands.
func (s *Sensor) Bands() []levels.Info { return []levels.Info{levels.NoBands(Resource, reasonUnit)} }

// Configure implements sensorcli.Sensor.
func (s *Sensor) Configure(cfg sensorcli.Config) error {
	if err := cfg.CheckKeys("sso_session", "cache_dir", "start_url", "margin"); err != nil {
		return err
	}
	session, err := cfg.String("sso_session", DefaultSession)
	if err != nil {
		return err
	}
	if session == "" {
		return fmt.Errorf("config key sso_session: must not be empty")
	}
	dir, err := cfg.String("cache_dir", "")
	if err != nil {
		return err
	}
	url, err := cfg.String("start_url", "")
	if err != nil {
		return err
	}
	margin, err := cfg.Duration("margin", DefaultMargin)
	if err != nil {
		return err
	}
	s.Session, s.CacheDir, s.StartURL, s.Margin = session, dir, url, margin
	return nil
}

// Measure implements sensorcli.Sensor.
func (s *Sensor) Measure(_ context.Context, env sensorcli.Env) ([]levels.Observation, error) {
	if env.Resource != "" && env.Resource != Resource {
		return nil, levels.ErrNoReading
	}
	level, left, why := s.verdict(env)
	if why != "" {
		fmt.Fprintf(env.Stderr, "aws-sso-sensor: %s: %s\n", Resource, why)
	}
	return []levels.Observation{{
		Resource:    Resource,
		Scale:       levels.Binary,
		Level:       level,
		Reading:     &levels.Reading{Value: left.Seconds(), Unit: reasonUnit},
		BypassDwell: env.Refresh,
	}}, nil
}

func (s *Sensor) dir(env sensorcli.Env) string {
	if s.CacheDir != "" {
		return s.CacheDir
	}
	if home := env.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".aws", "sso", "cache")
	}
	return ""
}

func sha1Name(s string) string {
	sum := sha1.Sum([]byte(s)) //nolint:gosec // see import
	return hex.EncodeToString(sum[:]) + ".json"
}

// candidate is the verdict of one matching file.
type candidate struct {
	ok   bool
	left time.Duration
}

func (s *Sensor) verdict(env sensorcli.Env) (string, time.Duration, string) {
	bad := levels.Binary.Worst()
	dir := s.dir(env)
	if dir == "" {
		return bad, 0, "no cache_dir configured and HOME is missing or empty"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return bad, 0, fmt.Sprintf("SSO cache directory unreadable: %v", err)
	}
	byName := sha1Name(s.Session)
	var best candidate
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var f cacheFile
		if json.Unmarshal(raw, &f) != nil {
			continue // malformed file: ignored
		}
		if !s.matches(e.Name() == byName, f) {
			continue
		}
		c, ok := s.judge(f, env.Now)
		if !ok {
			continue
		}
		if !found || (c.ok && !best.ok) || (c.ok == best.ok && c.left > best.left) {
			best, found = c, true
		}
	}
	switch {
	case !found:
		return bad, 0, "no usable SSO cache file matches the configured session"
	case !best.ok:
		return bad, best.left, fmt.Sprintf("SSO session cannot be renewed past %s from now (margin %s)", best.left.Truncate(time.Second), s.Margin)
	}
	return levels.Binary.Best(), best.left, ""
}

func (s *Sensor) matches(byName bool, f cacheFile) bool {
	if byName || f.SessionName == s.Session {
		return true
	}
	return s.StartURL != "" && f.StartURL == s.StartURL
}

// judge applies the usability rule to one file; ok=false means the file has no
// readable expiry and is ignored.
func (s *Sensor) judge(f cacheFile, now time.Time) (candidate, bool) {
	exp, ok := parseTime(f.ExpiresAt)
	if !ok {
		return candidate{}, false
	}
	accessLeft := exp.Sub(now)
	if f.RefreshToken {
		if reg, ok := parseTime(f.RegistrationAt); ok {
			left := floor0(reg.Sub(now))
			return candidate{ok: left >= s.Margin && left > 0, left: left}, true
		}
		// Refresh token with an unknown renewal horizon: usable.
		return candidate{ok: true, left: floor0(accessLeft)}, true
	}
	left := floor0(accessLeft)
	return candidate{ok: left >= s.Margin && left > 0, left: left}, true
}

func floor0(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	return d
}
