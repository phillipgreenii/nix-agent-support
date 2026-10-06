// repoident.go: the stable identity of a repository, used in entity ids and
// the repo:<repo_ident> label.
package internal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

// repoIdent returns the normalized origin URL as host/owner/name when the repo
// at repoPath has an origin remote, else the directory basename plus a short
// hash of the repo path. Two clones of one origin therefore share an ident.
func repoIdent(ctx context.Context, r Runner, repoPath string) string {
	if out, err := r.Run(ctx, repoPath, "config", "--get", "remote.origin.url"); err == nil {
		if ident := normalizeRemoteURL(out); ident != "" {
			return ident
		}
	}
	return fallbackIdent(repoPath)
}

// fallbackIdent is <basename>-<8 hex of sha256(path)>.
func fallbackIdent(repoPath string) string {
	clean := filepath.Clean(repoPath)
	sum := sha256.Sum256([]byte(clean))
	return filepath.Base(clean) + "-" + hex.EncodeToString(sum[:])[:8]
}

// normalizeRemoteURL turns a remote URL into host/owner/name: scheme, user
// info, port, a trailing slash and a trailing .git are dropped, and scp-style
// user@host:owner/name is handled. A local filesystem path or file:// URL
// (no host) normalizes to its path without the leading slash. It returns ""
// for an empty or unparseable value.
func normalizeRemoteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var host, p string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		host, p = u.Hostname(), u.Path
	case isScpStyle(raw):
		rest := raw
		if i := strings.Index(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		i := strings.Index(rest, ":")
		host, p = rest[:i], rest[i+1:]
	default:
		p = raw
	}
	p = strings.TrimSuffix(strings.TrimRight(p, "/"), ".git")
	p = strings.Trim(path.Clean("/"+p), "/")
	host = strings.ToLower(host)
	switch {
	case host != "" && p != "":
		return host + "/" + p
	case host != "":
		return host
	default:
		return p
	}
}

// isScpStyle reports whether s looks like [user@]host:path, i.e. it has a
// colon before any slash and is not a Windows-style or absolute path.
func isScpStyle(s string) bool {
	colon := strings.Index(s, ":")
	if colon <= 0 {
		return false
	}
	if slash := strings.Index(s, "/"); slash >= 0 && slash < colon {
		return false
	}
	return true
}
