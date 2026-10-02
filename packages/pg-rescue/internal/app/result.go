package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phillipgreenii/pg-rescue/internal/cli"
	"github.com/phillipgreenii/pg-rescue/internal/contract"
)

// runResult implements `pg-rescue result`: it prints exactly one valid result
// JSON object on stdout and exits with the code matching the outcome (0, 3 or
// 2). It is meant to be the last command of a handler. Any problem with its
// own arguments exits 70 and prints nothing on stdout.
func runResult(args []string, stdout, stderr io.Writer) int {
	opts, err := cli.ParseResult(args)
	if errors.Is(err, cli.ErrHelp) {
		fmt.Fprint(stdout, cli.Usage)
		return 0
	}
	if err != nil {
		return failf(stderr, "%v", err)
	}
	outcome, err := contract.ParseReportable(opts.Outcome)
	if err != nil {
		return failf(stderr, "result: %v", err)
	}

	res := contract.Result{Outcome: outcome, Summary: opts.Summary}
	if opts.DetailsFile != "" {
		b, err := os.ReadFile(opts.DetailsFile)
		if err != nil {
			return failf(stderr, "result: --details-file: %v", err)
		}
		res.Details = strings.ToValidUTF8(string(b), "�")
	}
	if opts.MetaFile != "" {
		b, err := os.ReadFile(opts.MetaFile)
		if err != nil {
			return failf(stderr, "result: --meta-file: %v", err)
		}
		if _, err := contract.ParseObject(b); err != nil {
			return failf(stderr, "result: --meta-file %s: must hold exactly one JSON object: %v", opts.MetaFile, err)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, b); err != nil {
			return failf(stderr, "result: --meta-file %s: %v", opts.MetaFile, err)
		}
		res.Meta = json.RawMessage(compact.Bytes())
	}

	out, err := res.Render()
	if err != nil {
		return failf(stderr, "result: %v", err)
	}
	if _, err := stdout.Write(out); err != nil {
		return failf(stderr, "result: cannot write stdout: %v", err)
	}
	code, _ := outcome.ExitCode()
	return code
}
