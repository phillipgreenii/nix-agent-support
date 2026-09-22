// deployrecord.go: resolves "unexpected change with no corresponding
// deploy record" [design: "pg-router-probe run checks" item 3] into a
// concrete yes/no. The deploy-record FILE FORMAT itself has no design
// citation (this packet's own implementation choice, like the snapshot
// file format): a plain text file, one expected hash per line, that a
// deploy pipeline is expected to append the freshly-deployed binary's own
// hash to. Absent entirely (the common case until a deploy pipeline
// writes one), every hash change is treated as unexpected — the safer
// default, since "no record" and "a record that doesn't mention this
// hash" both mean the same thing here.
package main

import (
	"bufio"
	"os"
	"strings"
)

// deployRecordAllows reports whether hash appears as one of
// deployRecordPath's own lines (blank lines and lines starting with '#'
// ignored, mirroring a shell-script-friendly comment convention). A
// missing/unreadable file is not an error here — it just means no hash is
// on record, so every real change is reported.
func deployRecordAllows(deployRecordPath, hash string) bool {
	if deployRecordPath == "" || hash == "" {
		return false
	}
	f, err := os.Open(deployRecordPath)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == hash {
			return true
		}
	}
	return false
}
