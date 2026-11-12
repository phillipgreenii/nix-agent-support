// auth.go: credential resolution for pg-connector-alert-pagerduty (design
// 10). The API key comes from the macOS Keychain (default service
// pd-schedule-manager, account pagerduty-api, both configurable), falling
// back to the PAGERDUTY_API_TOKEN environment variable. Credential resolution
// is each backend's own concern, so it stays here and is never shared (ADR
// 0062 principle 6); the existing Python pd-schedule-manager is only a source of patterns, not a dependency.
package internal

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const (
	// DefaultKeychainService and DefaultKeychainAccount name the Keychain item
	// pd-schedule-manager already uses.
	DefaultKeychainService = "pd-schedule-manager"
	DefaultKeychainAccount = "pagerduty-api"
	// TokenEnvVar is the fallback environment variable.
	TokenEnvVar = "PAGERDUTY_API_TOKEN"
)

// errNoToken means neither the Keychain nor the environment yielded a key.
var errNoToken = errors.New("no PagerDuty API key found (Keychain item or " + TokenEnvVar + ")")

// Keychain reads one generic password. It is the seam that keeps tests off the
// real Keychain. Any error means "not available" and falls through to the
// environment fallback.
type Keychain interface {
	Password(ctx context.Context, service, account string) (string, error)
}

// securityKeychain shells out to `security find-generic-password -w`, the same
// call pd-schedule-manager makes.
type securityKeychain struct{}

func (securityKeychain) Password(ctx context.Context, service, account string) (string, error) {
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-a", account, "-s", service, "-w").Output()
	if err != nil {
		return "", fmt.Errorf("keychain item %s/%s: %w", service, account, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// TokenResolver resolves the API key.
type TokenResolver struct {
	keychain Keychain
	getenv   func(string) string
}

// NewTokenResolver returns the production resolver (real Keychain, real env).
func NewTokenResolver(getenv func(string) string) *TokenResolver {
	return &TokenResolver{keychain: securityKeychain{}, getenv: getenv}
}

// Resolve returns the API key: Keychain first (unless disabled), then the
// environment. A missing key is errNoToken.
func (r *TokenResolver) Resolve(ctx context.Context, cfg backendConfig) (string, error) {
	if !cfg.DisableKeychain {
		if tok, err := r.keychain.Password(ctx, cfg.KeychainService, cfg.KeychainAccount); err == nil && tok != "" {
			return tok, nil
		}
	}
	if tok := strings.TrimSpace(r.getenv(TokenEnvVar)); tok != "" {
		return tok, nil
	}
	return "", errNoToken
}
