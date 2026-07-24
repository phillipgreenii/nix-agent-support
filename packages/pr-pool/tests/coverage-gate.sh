#!/usr/bin/env bash
# coverage-gate.sh — enforce per-package STATEMENT-coverage thresholds from a Go
# coverprofile. This is the enforcer the pr-pool Cluster-5 Go items (bead
# pg2-hvlyj .13/.16/.17/.18) cite by name via the `pr-pool-go-tests` flake check
# (bead pg2-hvlyj.19, plan item 5.7 / FIX 2).
#
# Why parse the profile directly instead of averaging `go tool cover -func`
# per-function percentages: `-func` reports a PERCENT per function, and functions
# hold different statement counts, so averaging those percents is NOT statement
# coverage. The coverprofile's own `<file>:<range> <numstmts> <count>` records ARE
# statement counts, so summing them per package (covered where count>0, over
# total) is the exact per-package statement coverage Go's `-cover` reports. We
# still print the profile's overall total for the human reading CI logs.
#
# Usage:
#   coverage-gate.sh <coverprofile> <thresholds-file>
#
# Thresholds-file format (one rule per line; `#` comments and blank lines
# ignored):
#   <package-import-suffix> <min-statement-percent>
# e.g.
#   internal/eventqueue 90
#   internal/msgschema  85
# A rule matches a package whose import path equals the suffix or ends with
# "/<suffix>". A gated package ABSENT from the profile is a FAILURE (guards
# against a renamed/removed/never-run package silently passing the bar).

set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: coverage-gate.sh <coverprofile> <thresholds-file>" >&2
  exit 2
fi

profile="$1"
thresholds="$2"

[ -f "$profile" ] || {
  echo "coverage-gate: profile not found: $profile" >&2
  exit 2
}
[ -f "$thresholds" ] || {
  echo "coverage-gate: thresholds file not found: $thresholds" >&2
  exit 2
}

awk -v thresholds="$thresholds" '
	BEGIN {
		# Load threshold rules: suffix -> min percent.
		n = 0
		while ((getline line < thresholds) > 0) {
			sub(/#.*/, "", line)                 # strip comments
			gsub(/^[ \t]+|[ \t]+$/, "", line)    # trim
			if (line == "") continue
			split(line, f, /[ \t]+/)
			if (f[1] == "" || f[2] == "") continue
			n++
			ruleSuffix[n] = f[1]
			ruleMin[n]    = f[2] + 0
			ruleMatched[n] = 0
		}
		close(thresholds)
	}
	# Coverprofile records: "<path>:<range> <numstmts> <count>". Skip the
	# "mode:" header and anything malformed.
	/^mode:/ { next }
	NF >= 3 {
		loc = $1
		# package = the path with the trailing "/<file>.go:<range>" removed.
		m = split(loc, a, "/")
		pkg = a[1]
		for (i = 2; i < m; i++) pkg = pkg "/" a[i]
		stmts = $2 + 0
		cnt   = $(NF) + 0          # execution count is the last field
		total[pkg] += stmts
		if (cnt > 0) covered[pkg] += stmts
		seen[pkg] = 1
	}
	END {
		# Compute per-package percentages once.
		for (pkg in seen) {
			pct[pkg] = (total[pkg] > 0) ? (100.0 * covered[pkg] / total[pkg]) : 100.0
		}

		fail = 0
		printf "coverage-gate: enforcing %d threshold rule(s)\n", n
		for (r = 1; r <= n; r++) {
			suffix = ruleSuffix[r]
			minPct = ruleMin[r]
			# Find the package(s) matching this rule.
			matchPkg = ""
			for (pkg in seen) {
				if (pkg == suffix || pkg ~ ("/" suffix "$")) {
					matchPkg = pkg
					ruleMatched[r] = 1
					if (pct[pkg] + 0.0000001 < minPct) {
						printf "  FAIL  %-45s %6.1f%% < %.0f%%\n", pkg, pct[pkg], minPct
						fail = 1
					} else {
						printf "  ok    %-45s %6.1f%% >= %.0f%%\n", pkg, pct[pkg], minPct
					}
				}
			}
			if (ruleMatched[r] == 0) {
				printf "  FAIL  gated package matching %s NOT FOUND in profile\n", suffix
				fail = 1
			}
		}
		if (fail) {
			print "coverage-gate: FAILED — one or more gated packages below threshold or missing"
			exit 1
		}
		print "coverage-gate: PASSED — all gated packages meet their thresholds"
	}
' "$profile"
