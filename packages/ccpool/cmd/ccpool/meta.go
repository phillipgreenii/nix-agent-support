package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/phillipgreenii/ccpool/internal/clock"
	"github.com/phillipgreenii/ccpool/internal/config"
	"github.com/phillipgreenii/ccpool/internal/store"
)

// parseMetaArgs validates `meta <verb> <external_id> [key] [value]`. set takes a
// key and an OPTIONAL value (default ""); get/rm take a key; list takes neither.
// Pure (no I/O) so it is unit-testable.
func parseMetaArgs(args []string) (verb, externalID, key, value string, err error) {
	if len(args) < 2 {
		return "", "", "", "", fmt.Errorf("usage: ccpool meta <set|get|list|rm> <external_id> [key] [value]")
	}
	verb, externalID = args[0], args[1]
	rest := args[2:]
	switch verb {
	case "set":
		if len(rest) < 1 {
			return "", "", "", "", fmt.Errorf("usage: ccpool meta set <external_id> <key> [value]")
		}
		key = rest[0]
		if len(rest) >= 2 {
			value = strings.Join(rest[1:], " ")
		}
	case "get", "rm":
		if len(rest) != 1 {
			return "", "", "", "", fmt.Errorf("usage: ccpool meta %s <external_id> <key>", verb)
		}
		key = rest[0]
	case "list":
		if len(rest) != 0 {
			return "", "", "", "", fmt.Errorf("usage: ccpool meta list <external_id> [--json]")
		}
	default:
		return "", "", "", "", fmt.Errorf("ccpool meta: unknown verb %q (want set|get|list|rm)", verb)
	}
	return verb, externalID, key, value, nil
}

// parseMetaFlags pulls a trailing --json (only meaningful for `list`) and any
// repeatable --label <key> (only meaningful for `set`; consumes the following
// token) out of args, before parseMetaArgs's positional parse. meta.go
// hand-parses positional args rather than using flag.FlagSet (unlike new.go),
// so this pre-scan is where any interspersed flag has to be pulled out
// (pg2-24f89/pg2-qye99 D8.1 — this file has no flag.FlagSet pattern to copy
// from new.go). Pure (no I/O) so it is unit-testable.
func parseMetaFlags(args []string) (jsonOut bool, labels labelFlag, pos []string, err error) {
	labels = labelFlag{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--json":
			jsonOut = true
			continue
		case "--label":
			if i+1 >= len(args) {
				return false, nil, nil, fmt.Errorf("ccpool meta: --label requires a value")
			}
			i++
			if err := labels.Set(args[i]); err != nil {
				return false, nil, nil, err
			}
			continue
		}
		pos = append(pos, a)
	}
	return jsonOut, labels, pos, nil
}

// renderMetaList renders metadata as sorted "key=value\n" lines. Pure.
func renderMetaList(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, m[k])
	}
	return b.String()
}

// renderMetaListJSON marshals metadata as a JSON object (deterministic: Go marshals
// map string keys sorted). Pure.
func renderMetaListJSON(m map[string]string) (string, error) {
	if m == nil {
		m = map[string]string{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func runMeta(args []string) int {
	jsonOut, labels, pos, err := parseMetaFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	verb, externalID, key, value, err := parseMetaArgs(pos)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("meta: config load failed", "err", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		slog.Error("meta: store open failed", "err", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()

	switch verb {
	case "set":
		if err := st.SetMeta(ctx, externalID, key, value); err != nil {
			slog.Error("meta: set failed", "err", err)
			return 1
		}
		// --label marks each named key as label-eligible in this SAME
		// invocation, right after the metadata write above commits
		// (pg2-24f89/pg2-qye99 D8.1).
		if err := applyLabels(st, externalID, labels); err != nil {
			slog.Error("meta: label failed", "err", err)
			return 1
		}
	case "get":
		v, ok, err := st.GetMeta(ctx, externalID, key)
		if err != nil {
			slog.Error("meta: get failed", "err", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "no such metadata key")
			return 1
		}
		fmt.Println(v)
	case "rm":
		if err := st.DeleteMeta(ctx, externalID, key); err != nil {
			slog.Error("meta: rm failed", "err", err)
			return 1
		}
	case "list":
		m, err := st.Meta(ctx, externalID)
		if err != nil {
			slog.Error("meta: list failed", "err", err)
			return 1
		}
		if jsonOut {
			out, err := renderMetaListJSON(m)
			if err != nil {
				slog.Error("meta: list json render failed", "err", err)
				return 1
			}
			fmt.Println(out)
		} else {
			fmt.Print(renderMetaList(m))
		}
	}
	return 0
}
