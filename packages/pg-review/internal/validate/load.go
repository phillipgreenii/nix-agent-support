package validate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Load reads the run's inputs from its evidence directory (evidence.json,
// query-log.jsonl, query-results/Q-nnn.json) and its staged raw text. An
// empty stagingPath means no raw text. The returned Input has no Doc, Kinds
// or limits; the caller fills them in.
func Load(evidenceDir, stagingPath string) (Input, error) {
	var in Input
	b, err := os.ReadFile(filepath.Join(evidenceDir, "evidence.json"))
	if err != nil {
		return in, err
	}
	if err := json.Unmarshal(b, &in.Evidence); err != nil {
		return in, fmt.Errorf("evidence.json: %w", err)
	}
	f, err := os.Open(filepath.Join(evidenceDir, "query-log.jsonl"))
	if err != nil {
		return in, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var q QueryEntry
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			return in, fmt.Errorf("query-log.jsonl: %w", err)
		}
		in.QueryLog = append(in.QueryLog, q)
	}
	if err := sc.Err(); err != nil {
		return in, err
	}
	in.QueryResults = map[string]QueryResult{}
	files, err := filepath.Glob(filepath.Join(evidenceDir, "query-results", "Q-*.json"))
	if err != nil {
		return in, err
	}
	for _, p := range files {
		rb, err := os.ReadFile(p)
		if err != nil {
			return in, err
		}
		var r QueryResult
		if err := json.Unmarshal(rb, &r); err != nil {
			return in, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		in.QueryResults[strings.TrimSuffix(filepath.Base(p), ".json")] = r
	}
	if stagingPath != "" {
		raw, err := readTree(stagingPath)
		if err != nil {
			return in, err
		}
		in.RawText = raw
	}
	return in, nil
}

// readTree reads a file, or every regular file under a directory, as text.
func readTree(p string) (string, error) {
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		b, err := os.ReadFile(p)
		return string(b), err
	}
	var sb strings.Builder
	err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sb.Write(b)
		sb.WriteByte('\n')
		return nil
	})
	return sb.String(), err
}
