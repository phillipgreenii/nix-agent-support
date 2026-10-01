package threadref

import (
	"reflect"
	"testing"
)

func TestScanPermalinks(t *testing.T) {
	text := "see https://github.com/acme/widgets/pull/12, and /pull/7. again /pull/12 done"
	got := ScanPermalinks(text, "acme/widgets")
	want := []string{"acme/widgets#12", "acme/widgets#7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := ScanPermalinks("nothing here", "acme/widgets"); got != nil {
		t.Fatalf("got %v want nil", got)
	}
}
