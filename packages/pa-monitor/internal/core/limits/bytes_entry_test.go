package limits

import "testing"

func TestParseStatusRecordsSkipsBadAndTSLess(t *testing.T) {
	data := []byte("{\"ts\":1}\nnot json\n{\"x\":1}\n\n{\"ts\":2}")
	if got := ParseStatusRecords(data); len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
}
