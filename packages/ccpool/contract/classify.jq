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
# Usage:
#   go test -tags contract -timeout=0 -p 1 -json ./cmd/ccpool/... \
#     | jq -r -f contract/classify.jq | sort | uniq -c

select(.Action == "output")
| .Output
| capture("OUTCOME=(?<bucket>[a-z-]+)").bucket
