package config

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// TemplateInput is the worktree information a directory-name template sees.
type TemplateInput struct {
	Index                      int
	Repo, Name, LeaseID, Owner string
	Metadata                   map[string]string
	Min, Max, Count            int
}

// letter renders 0 as a, 25 as z, 26 as aa.
func letter(n int) string {
	if n < 0 {
		n = 0
	}
	n++
	var b []byte
	for n > 0 {
		n--
		b = append([]byte{byte('a' + n%26)}, b...)
		n /= 26
	}
	return string(b)
}

// RenderDir renders a worktree directory-name template. Functions repo, index,
// name, lease_id, owner, min, max and count take no argument, metadata takes a
// key, and letter is for pipes. An empty result, or one that is not a single
// path component, fails.
func RenderDir(tmpl string, in TemplateInput) (string, error) {
	t, err := template.New("dir").Option("missingkey=zero").Funcs(template.FuncMap{
		"repo":     func() string { return in.Repo },
		"index":    func() int { return in.Index },
		"name":     func() string { return in.Name },
		"lease_id": func() string { return in.LeaseID },
		"owner":    func() string { return in.Owner },
		"min":      func() int { return in.Min },
		"max":      func() int { return in.Max },
		"count":    func() int { return in.Count },
		"metadata": func(k string) string { return in.Metadata[k] },
		"letter":   letter,
	}).Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("template %q: %w", tmpl, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, nil); err != nil {
		return "", fmt.Errorf("template %q: %w", tmpl, err)
	}
	out := buf.String()
	switch {
	case out == "" || out == "." || out == "..":
		return "", fmt.Errorf("template %q renders %q, not a directory name", tmpl, out)
	case strings.ContainsAny(out, "/\x00"):
		return "", fmt.Errorf("template %q renders %q, which is not a single directory name", tmpl, out)
	}
	return out, nil
}
