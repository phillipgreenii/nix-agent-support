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
