# classify.jq — turn `go test -json` into the contract outcome tally.
#
# Each line of `go test -json` is a JSON object. Test log lines (t.Logf/t.Errorf/
# t.Skipf) surface as {"Action":"output","Output":"... OUTCOME=<bucket> ..."}.
# The harness emits exactly one OUTCOME=<bucket> token per judgement; this filter
# extracts that bucket token and prints it on its own line, so a downstream
# `sort | uniq -c` yields a per-bucket count.
#
# Buckets (see contract/README.md): live, baseline, baseline-drift, live-fail,
# scaffold, pending. Any baseline-drift / live-fail / scaffold count means
# investigate.
#
# Classifier blind spot (pg2-hh8fk): a test can FAIL without ever emitting an
# OUTCOME= line — e.g. ccpTimed's subprocess-timeout guard in
# contract_harness_test.go fails via a plain t.Fatalf hang-timeout message, not
# via one of the OUTCOME-emitting helpers. Such a failure would otherwise be
# invisible to the tally: the overall `go test` result is FAIL, but the printed
# buckets could look clean. To catch this, this filter tracks per-test whether
# any OUTCOME= line was seen, and synthesizes an `unclassified-fail` bucket
# entry for any test-level "fail" action that never emitted one, so it always
# shows up in the tally: any count > 0 means investigate, exactly like
# baseline-drift / live-fail / scaffold.
#
# Usage (note -n: this filter reads the whole stream via `inputs` to carry
# per-test state, so it must run in null-input mode):
#   go test -tags contract -timeout=0 -p 1 -json ./cmd/ccpool/... \
#     | jq -n -r -f contract/classify.jq | sort | uniq -c

foreach inputs as $e (
  {seen: {}};
  if $e.Action == "output" and $e.Test != null
     and (($e.Output // "") | test("OUTCOME="))
  then .seen[$e.Test] = true
  else . end;
  if $e.Action == "output" then
    ($e.Output // "" | capture("OUTCOME=(?<bucket>[a-z-]+)").bucket)
  elif $e.Action == "fail" and $e.Test != null and (.seen[$e.Test] // false | not) then
    "unclassified-fail"
  else
    empty
  end
)
