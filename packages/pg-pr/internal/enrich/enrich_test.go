package enrich

import "testing"

func TestBucketSize(t *testing.T) {
	cases := []struct {
		total int
		want  string
	}{
		{0, "XS"}, {9, "XS"}, {10, "S"}, {29, "S"}, {30, "M"},
		{99, "M"}, {100, "L"}, {499, "L"}, {500, "XL"}, {5000, "XL"},
	}
	for _, c := range cases {
		if got := bucketSize(c.total); got != c.want {
			t.Errorf("bucketSize(%d) = %q; want %q", c.total, got, c.want)
		}
	}
}

func TestClassifyKind(t *testing.T) {
	cases := []struct {
		name    string
		title   string
		branch  string
		commits []string
		want    string
	}{
		{"title conventional fix", "fix(store): wrong scan", "anything", nil, "bugfix"},
		{"title feat with bang", "feat!: breaking change", "x", nil, "feature"},
		{"branch prefix when title plain", "tidy things up", "refactor/cleanup", nil, "refactor"},
		{"branch feature alias", "stuff", "feature/new-ui", nil, "feature"},
		{"commit majority when title+branch plain", "wip", "wip", []string{"fix: a", "fix: b", "docs: c"}, "bugfix"},
		{"fallback other", "random work", "wip", nil, "other"},
		{"title wins over branch", "docs: readme", "fix/typo", nil, "docs"},
	}
	for _, c := range cases {
		if got := classifyKind(c.title, c.branch, c.commits); got != c.want {
			t.Errorf("%s: classifyKind(%q,%q,%v) = %q; want %q", c.name, c.title, c.branch, c.commits, got, c.want)
		}
	}
}
