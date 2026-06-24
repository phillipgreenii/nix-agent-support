package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Pull a trailing --json (only meaningful for `list`) before positional parse.
	jsonOut := false
	var pos []string
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
			continue
		}
		pos = append(pos, a)
	}
	verb, externalID, key, value, err := parseMetaArgs(pos)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return 1
	}
	st, err := store.Open(cfg.DBPath, clock.Real{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "store:", err)
		return 1
	}
	defer st.Close()
	ctx := context.Background()

	switch verb {
	case "set":
		if err := st.SetMeta(ctx, externalID, key, value); err != nil {
			fmt.Fprintln(os.Stderr, "meta set:", err)
			return 1
		}
	case "get":
		v, ok, err := st.GetMeta(ctx, externalID, key)
		if err != nil {
			fmt.Fprintln(os.Stderr, "meta get:", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "no such metadata key")
			return 1
		}
		fmt.Println(v)
	case "rm":
		if err := st.DeleteMeta(ctx, externalID, key); err != nil {
			fmt.Fprintln(os.Stderr, "meta rm:", err)
			return 1
		}
	case "list":
		m, err := st.Meta(ctx, externalID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "meta list:", err)
			return 1
		}
		if jsonOut {
			out, err := renderMetaListJSON(m)
			if err != nil {
				fmt.Fprintln(os.Stderr, "meta list:", err)
				return 1
			}
			fmt.Println(out)
		} else {
			fmt.Print(renderMetaList(m))
		}
	}
	return 0
}
