package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// The fake generator is this test binary re-executed: TestMain reads
// WR_FAKE_GEN and behaves as a generator instead of running tests. It needs
// no separate build and works on macOS and Linux. Its registration MUST list
// WR_FAKE_GEN (and WR_FAKE_DUMP) in its env, because the environment is an
// allowlist. Which behavior it shows is the preset config's "fake" key, so
// one generator registration serves presets that need different behaviors.
//
// A second re-execution runs main() itself (WR_AS_MAIN=1), so the exit-code
// test drives the real command line, process exit status included.

const (
	envFakeGen = "WR_FAKE_GEN"
	envDump    = "WR_FAKE_DUMP"
	envAsMain  = "WR_AS_MAIN"
)

// fakeDump is what the fake generator writes to WR_FAKE_DUMP: the envelope it
// was given, the environment it saw and its working directory.
type fakeDump struct {
	Env      []string       `json:"env"`
	Cwd      string         `json:"cwd"`
	Envelope map[string]any `json:"envelope"`
}

const fakeManifest = `{"contract_version":1,"files":[{"path":"report.md","content_type":"text/markdown","title":"Fake"}],` +
	`"summary":"fake summary","warnings":["fake warning"]}`

func fakeGenMain() int {
	raw, _ := io.ReadAll(os.Stdin)
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		fmt.Fprintln(os.Stderr, "fake generator: bad envelope:", err)
		return 2
	}
	if path := os.Getenv(envDump); path != "" {
		cwd, _ := os.Getwd()
		b, _ := json.Marshal(fakeDump{Env: os.Environ(), Cwd: cwd, Envelope: env})
		_ = os.WriteFile(path, b, 0o600)
	}
	cfg, _ := env["config"].(map[string]any)
	behavior, _ := cfg["fake"].(string)
	validate := env["mode"] == "validate"

	switch behavior {
	case "reject":
		fmt.Fprintln(os.Stderr, "promptFile: cannot read /nonexistent/addendum.txt")
		fmt.Fprintln(os.Stderr, "model: unknown")
		return 3
	case "crash":
		fmt.Fprintln(os.Stderr, "boom: entry text echoed")
		return 1
	case "needsconfig":
		if len(cfg) == 0 {
			fmt.Fprintln(os.Stderr, "model is required")
			return 3
		}
	case "hang":
		time.Sleep(time.Minute)
	case "dirty":
		if validate {
			_ = os.WriteFile("stray.txt", []byte("x"), 0o600)
			return 0
		}
	}
	if validate {
		return 0
	}
	switch behavior {
	case "twofiles":
		_ = os.WriteFile("a.md", []byte("# a\n"), 0o600)
		_ = os.WriteFile("b.md", []byte("# b\n"), 0o600)
		fmt.Print(`{"contract_version":1,"files":[{"path":"a.md","content_type":"text/markdown"},{"path":"b.md","content_type":"text/markdown"}]}`)
		return 0
	case "binary":
		_ = os.WriteFile("report.bin", []byte{0, 1, 2}, 0o600)
		fmt.Print(`{"contract_version":1,"files":[{"path":"report.bin","content_type":"application/octet-stream"}]}`)
		return 0
	case "big", "exact":
		n := printLimit
		if behavior == "big" {
			n++
		}
		_ = os.WriteFile("report.md", []byte(strings.Repeat("x", n)), 0o600)
		fmt.Print(`{"contract_version":1,"files":[{"path":"report.md","content_type":"text/markdown"}]}`)
		return 0
	}
	_ = os.WriteFile("report.md", []byte("# fake report\n"), 0o600)
	fmt.Print(fakeManifest)
	return 0
}
