package config

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// BuildEnv computes a handler's environment. Precedence, lowest to highest:
//
//  1. base (the wrapper's own environment, as KEY=VALUE entries)
//  2. the handler's env
//  3. the handler's env_file
//  4. wrapperVars (the PG_RESCUE_* variables the wrapper sets)
//
// The env_file is read here, i.e. when the handler is spawned. The result is
// sorted by key so it is deterministic.
func BuildEnv(base []string, h *Handler, wrapperVars map[string]string) ([]string, error) {
	m := map[string]string{}
	for _, kv := range base {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			m[k] = v
		}
	}
	for k, v := range h.Env {
		m[k] = v
	}
	if h.EnvFile != "" {
		fileVars, err := ReadEnvFile(h.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("[handler.%s] key \"env_file\": %w", h.Name, err)
		}
		for k, v := range fileVars {
			m[k] = v
		}
	}
	for k, v := range wrapperVars {
		m[k] = v
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + "=" + m[k]
	}
	return out, nil
}

// ReadEnvFile reads a dotenv file: KEY=VALUE lines, optional leading
// "export ", "#" comments, blank lines, and single- or double-quoted values
// (double quotes understand \n, \t, \" and \\). There is no interpolation.
// The permission check is repeated on the open file descriptor, so a file
// that was tightened at validation time and loosened since is still refused.
func ReadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s has unsafe permissions %04o: it is group- or world-accessible; run chmod 600 on it",
			path, fi.Mode().Perm())
	}
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			return nil, fmt.Errorf("%s line %d: expected KEY=VALUE", path, n)
		}
		val, err := unquote(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %v", path, n, err)
		}
		out[k] = val
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

func unquote(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	switch v[0] {
	case '\'':
		if len(v) < 2 || v[len(v)-1] != '\'' {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		return v[1 : len(v)-1], nil
	case '"':
		if len(v) < 2 || v[len(v)-1] != '"' {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		var b strings.Builder
		body := v[1 : len(v)-1]
		for i := 0; i < len(body); i++ {
			c := body[i]
			if c != '\\' || i+1 == len(body) {
				b.WriteByte(c)
				continue
			}
			i++
			switch body[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case '"', '\\':
				b.WriteByte(body[i])
			default:
				b.WriteByte('\\')
				b.WriteByte(body[i])
			}
		}
		return b.String(), nil
	}
	// Unquoted: an inline " #" starts a comment.
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v, nil
}
