package telemetry

import (
	"context"
	"sort"
	"strings"
	"testing"
)

func TestAuthDegradedHealthyIsOneNoneSeriesAtZero(t *testing.T) {
	f := newFixture(t, nil)
	got := points(t, f.metric(MetricAuthDegraded))
	if len(got) != 1 || got["reason=none"] != 0 {
		t.Fatalf("auth.degraded = %v, want only reason=none at 0", got)
	}
}

func TestAuthDegradedReportsOneSeriesPerReasonAtOneAndDropsTheNoneSeries(t *testing.T) {
	for _, reason := range []string{"token_rejected", "token_unavailable", "token_file"} {
		t.Run(reason, func(t *testing.T) {
			f := newFixture(t, nil)
			f.tokenDegraded, f.tokenReason = true, reason
			got := points(t, f.metric(MetricAuthDegraded))
			if len(got) != 1 || got["reason="+reason] != 1 {
				t.Fatalf("auth.degraded = %v, want only reason=%s at 1", got, reason)
			}
			// Recovery (a restart in production) returns to the none series.
			f.tokenDegraded, f.tokenReason = false, ""
			if got := points(t, f.metric(MetricAuthDegraded)); len(got) != 1 || got["reason=none"] != 0 {
				t.Errorf("after recovery auth.degraded = %v", got)
			}
		})
	}
}

func TestAuthDegradedFoldsAnUnknownReasonToOther(t *testing.T) {
	f := newFixture(t, nil)
	f.tokenDegraded, f.tokenReason = true, "weird: /private/path"
	got := points(t, f.metric(MetricAuthDegraded))
	if len(got) != 1 || got["reason=other"] != 1 {
		t.Fatalf("auth.degraded = %v, want reason=other at 1", got)
	}
}

func TestAuthDegradedIsAbsentWhenTheTokenHandoffIsOff(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.TokenState = nil })
	if _, ok := f.collect()[MetricAuthDegraded]; ok {
		t.Error("auth.degraded reported with no TokenState source")
	}
}

func TestTokenReadsCountsByReasonResultAndErrorKindWithinTheEnums(t *testing.T) {
	f := newFixture(t, nil)
	f.tel.TokenRead("start", "changed", "none")
	f.tel.TokenRead("auth_error", "unchanged", "locked") // kind dropped: not an error
	f.tel.TokenRead("auth_error", "error", "locked")
	f.tel.TokenRead("backoff", "error", "item_missing")
	f.tel.TokenRead("backoff", "error", "timeout")
	f.tel.TokenRead("backoff", "error", "exit status 7: /secret/path") // folds to other
	f.tel.TokenRead("surprise", "huh", "x")                            // folds to other, error, other
	got := points(t, f.metric(MetricTokenReads))
	want := map[string]float64{
		"error_kind=none,reason=start,result=changed":         1,
		"error_kind=none,reason=auth_error,result=unchanged":  1,
		"error_kind=locked,reason=auth_error,result=error":    1,
		"error_kind=item_missing,reason=backoff,result=error": 1,
		"error_kind=timeout,reason=backoff,result=error":      1,
		"error_kind=other,reason=backoff,result=error":        1,
		"error_kind=other,reason=other,result=error":          1,
	}
	if len(got) != len(want) {
		t.Errorf("series = %v", sortedKeys(got))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v (have %v)", k, got[k], v, sortedKeys(got))
		}
	}
}

func TestTokenFileOpsCountsWriteAndRemoveByResult(t *testing.T) {
	f := newFixture(t, nil)
	f.tel.TokenFileOp("write", "ok")
	f.tel.TokenFileOp("write", "ok")
	f.tel.TokenFileOp("write", "error")
	f.tel.TokenFileOp("remove", "ok")
	f.tel.TokenFileOp("remove", "error")
	f.tel.TokenFileOp("/tmp/x", "nope") // folds
	got := points(t, f.metric(MetricTokenFileOps))
	want := map[string]float64{
		"op=write,result=ok": 2, "op=write,result=error": 1,
		"op=remove,result=ok": 1, "op=remove,result=error": 1,
		"op=other,result=error": 1,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("series = %v", sortedKeys(got))
	}
}

func TestNilTelemetryTokenMethodsAreNoOps(t *testing.T) {
	var tel *Telemetry
	tel.TokenRead("start", "changed", "none")
	tel.TokenFileOp("write", "ok")
	_ = context.Background()
}

func sortedKeys(m map[string]float64) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, " | ")
}
