package detectors

import "testing"

func TestNormaliseGitOrigin(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owner/repo.git":      "github.com/owner/repo",
		"https://github.com/owner/repo.git":  "github.com/owner/repo",
		"https://github.com/owner/repo":      "github.com/owner/repo",
		"ssh://git@github.com/owner/repo":    "github.com/owner/repo",
		"git@gitlab.com:group/sub/repo.git":  "gitlab.com/group/sub/repo",
		"http://internal.example/owner/repo": "internal.example/owner/repo",
	}
	for in, want := range cases {
		if got := normaliseOrigin(in); got != want {
			t.Errorf("normaliseOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}
