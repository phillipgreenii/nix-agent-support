package changes

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/gather"
)

func truncatedListing() gather.ListFingerprintsResult {
	res := okListing()
	res.Truncated = true
	return res
}

func TestRunPersistsOneListingStatusPerConsultedQuery(t *testing.T) {
	e, st, _ := newListEngine(t, "okq", "degq", "failq")
	e.Lister = queryLister{
		"okq":   {res: okListing()},
		"degq":  {res: truncatedListing()},
		"failq": {err: errors.New("boom\nsecond line")},
	}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	got, err := ReadListingStatuses(st, "issue")
	if err != nil {
		t.Fatal(err)
	}
	at := "2026-10-01T12:00:00Z"
	want := []ListingStatus{
		{Query: "degq", Status: StatusDegraded, Reason: ReasonListTruncated, At: at},
		{Query: "failq", Status: StatusFailed, Reason: "boom", At: at},
		{Query: "okq", Status: StatusOK, Reason: "", At: at},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statuses = %+v\nwant     %+v", got, want)
	}
	raw, found, _ := st.GetMeta("change_flow.listing.issue.degq")
	if !found || raw != `{"status":"degraded","reason":"`+ReasonListTruncated+`","at":"`+at+`"}` {
		t.Errorf("raw value = %q (found %v)", raw, found)
	}
}

func TestRunOverwritesAFailedListingStatusWithOK(t *testing.T) {
	e, st, _ := newListEngine(t, "open", "other")
	e.Lister = queryLister{"open": {err: errors.New("down")}, "other": {res: okListing()}}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadListingStatuses(st, "issue"); len(got) != 2 || got[0].Query != "open" || got[0].Status != StatusFailed {
		t.Fatalf("after failure = %+v", got)
	}
	e.Now = func() time.Time { return time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC) }
	e.Lister = fakeLister{res: okListing()}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	got, _ := ReadListingStatuses(st, "issue")
	if len(got) != 2 || got[0] != (ListingStatus{Query: "open", Status: StatusOK, At: "2026-10-01T13:00:00Z"}) {
		t.Fatalf("after recovery = %+v", got)
	}
}

func TestTotalFailureStillPersistsListingStatuses(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	e.Lister = fakeLister{err: errors.New("down")}
	if _, _, err := runTick(t, e); !errors.Is(err, ErrTotalFailure) {
		t.Fatalf("err = %v", err)
	}
	got, _ := ReadListingStatuses(st, "issue")
	if len(got) != 1 || got[0].Status != StatusFailed || got[0].Reason != "down" {
		t.Fatalf("statuses = %+v", got)
	}
}

func TestCachedCallWritesNoListingStatus(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	e.Lister = panicLister{}
	if _, _, err := runTick(t, e, Options{EntityType: "issue", Consumer: "router", Cached: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadListingStatuses(st, "issue"); len(got) != 0 {
		t.Errorf("--cached wrote %+v", got)
	}
	if all, _ := st.ListMetaPrefix("change_flow.listing."); len(all) != 0 {
		t.Errorf("--cached wrote meta %v", all)
	}
}

func TestQueryFlagLeavesOtherQueriesListingUntouched(t *testing.T) {
	e, st, _ := newListEngine(t, "a", "b")
	e.Lister = queryLister{"a": {err: errors.New("down")}, "b": {res: okListing()}}
	if _, _, err := runTick(t, e); err != nil {
		t.Fatal(err)
	}
	before, _ := ReadListingStatuses(st, "issue")

	e.Now = func() time.Time { return time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC) }
	e.Lister = queryLister{"a": {res: okListing()}, "b": {err: errors.New("must not be consulted")}}
	if _, _, err := runTick(t, e, Options{EntityType: "issue", Consumer: "router", Query: "a"}); err != nil {
		t.Fatal(err)
	}
	after, _ := ReadListingStatuses(st, "issue")
	if len(after) != 2 || after[0].Query != "a" || after[0].Status != StatusOK || after[0].At != "2026-10-01T14:00:00Z" {
		t.Fatalf("a = %+v", after)
	}
	if after[1] != before[1] {
		t.Errorf("b changed: %+v -> %+v", before[1], after[1])
	}
}

func TestListingWriteFailureNeverFailsTheCall(t *testing.T) {
	e, st, _ := newListEngine(t, "open")
	var warn bytes.Buffer
	e.Warn = &warn
	e.Lister = fakeLister{res: okListing()}
	e.setMeta = func(string, string) error { return errors.New("disk full") }
	env, out, err := runTick(t, e)
	if err != nil || out.Partial || len(env.Sources) != 1 || env.Sources[0].Status != StatusOK {
		t.Fatalf("env = %+v out = %+v err = %v", env, out, err)
	}
	if got, _ := ReadListingStatuses(st, "issue"); len(got) != 0 {
		t.Errorf("wrote %+v despite the failing setter", got)
	}
	if !bytes.Contains(warn.Bytes(), []byte("persist listing status")) {
		t.Errorf("no warning: %q", warn.String())
	}
}

func TestReadListingStatusesSkipsUndecodableAndScopesToType(t *testing.T) {
	_, st, _ := newListEngine(t, "open")
	for k, v := range map[string]string{
		"change_flow.listing.issue.zeta":  `{"status":"ok","reason":"","at":"t1"}`,
		"change_flow.listing.issue.alpha": `{"status":"failed","reason":"r","at":"t2"}`,
		"change_flow.listing.issue.bad":   `not json`,
		"change_flow.listing.issue.empty": `{"reason":"x"}`,
		"change_flow.listing.pr.mine":     `{"status":"ok","reason":"","at":"t3"}`,
	} {
		if err := st.SetMeta(k, v); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadListingStatuses(st, "issue")
	if err != nil {
		t.Fatal(err)
	}
	want := []ListingStatus{
		{Query: "alpha", Status: "failed", Reason: "r", At: "t2"},
		{Query: "zeta", Status: "ok", At: "t1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if none, err := ReadListingStatuses(st, "thread"); err != nil || len(none) != 0 {
		t.Errorf("thread = %+v, %v", none, err)
	}
}
