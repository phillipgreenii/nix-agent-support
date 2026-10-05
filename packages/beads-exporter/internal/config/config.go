// Package config loads and validates the exporter's configuration file.
//
// Validation is schema-only on purpose: it never touches the filesystem beyond
// reading the file itself (no stat of any beads directory, bd binary or Claude
// directory) and never spawns a process, because the check runs inside the nix
// build sandbox where none of those paths exist.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/queue"
)

// QueueSpec is one queue as it appears in the file: a name and the tokenised
// bd ready flag list. No other fields are allowed.
type QueueSpec struct {
	Name string   `json:"name"`
	Args []string `json:"args"`
}

// File is the on-disk schema.
type File struct {
	BDPath                  string            `json:"bdPath"`
	ChildPath               string            `json:"childPath"`
	BeadsDirs               map[string]string `json:"beadsDirs"`
	ClaudeDir               string            `json:"claudeDir"`
	OperatorNames           []string          `json:"operatorNames"`
	Port                    int               `json:"port"`
	PollIntervalSeconds     int               `json:"pollIntervalSeconds"`
	StrandedIntervalSeconds int               `json:"strandedIntervalSeconds"`
	StaleClaimHours         int               `json:"staleClaimHours"`
	CommandTimeoutSeconds   int               `json:"commandTimeoutSeconds"`
	LabelCap                int               `json:"labelCap"`
	Queues                  []QueueSpec       `json:"queues"`
}

// Config is a validated configuration with derived values resolved.
type Config struct {
	File
	// DBNames is the sorted list of database names.
	DBNames []string
	// ClassifiedQueues holds the queues in file order, classified.
	ClassifiedQueues []queue.Queue
}

// PollInterval is the main pass period.
func (c *Config) PollInterval() time.Duration {
	return time.Duration(c.PollIntervalSeconds) * time.Second
}

// StrandedInterval is the throughput (and later stranded) pass period.
func (c *Config) StrandedInterval() time.Duration {
	return time.Duration(c.StrandedIntervalSeconds) * time.Second
}

// CommandTimeout bounds each bd call.
func (c *Config) CommandTimeout() time.Duration {
	return time.Duration(c.CommandTimeoutSeconds) * time.Second
}

var (
	dbNameRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	queueNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// Load reads and validates the file at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

// Parse decodes and validates raw JSON. Unknown fields are rejected.
func Parse(raw []byte) (*Config, error) {
	var f File
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("decode config: trailing data after the top-level object")
	}
	return Validate(f)
}

// Validate checks f and resolves derived values. It collects every problem so
// one run reports them all.
func Validate(f File) (*Config, error) {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	requireAbs := func(field, v string) {
		switch {
		case v == "":
			add("%s is required", field)
		case !filepath.IsAbs(v):
			add("%s must be an absolute path, got %q", field, v)
		}
	}
	requirePositive := func(field string, v int) {
		if v <= 0 {
			add("%s must be a positive integer, got %d", field, v)
		}
	}

	requireAbs("bdPath", f.BDPath)
	requireAbs("claudeDir", f.ClaudeDir)

	if f.ChildPath == "" {
		add("childPath is required")
	} else {
		for _, p := range strings.Split(f.ChildPath, ":") {
			if p == "" || !filepath.IsAbs(p) {
				add("childPath entries must be absolute directories, got %q", p)
			}
		}
	}

	if len(f.BeadsDirs) == 0 {
		add("beadsDirs must name at least one database")
	}
	names := make([]string, 0, len(f.BeadsDirs))
	for name, dir := range f.BeadsDirs {
		if !dbNameRE.MatchString(name) {
			add("beadsDirs key %q is not a valid database name", name)
		}
		requireAbs(fmt.Sprintf("beadsDirs[%q]", name), dir)
		names = append(names, name)
	}
	sort.Strings(names)

	if f.OperatorNames == nil {
		add("operatorNames is required (it may be an empty list)")
	}
	seenOp := map[string]bool{}
	for _, n := range f.OperatorNames {
		if strings.TrimSpace(n) == "" {
			add("operatorNames entries must be non-empty")
		}
		if seenOp[n] {
			add("operatorNames entry %q is duplicated", n)
		}
		seenOp[n] = true
	}

	if f.Port < 1 || f.Port > 65535 {
		add("port must be between 1 and 65535, got %d", f.Port)
	}
	requirePositive("pollIntervalSeconds", f.PollIntervalSeconds)
	requirePositive("strandedIntervalSeconds", f.StrandedIntervalSeconds)
	requirePositive("staleClaimHours", f.StaleClaimHours)
	requirePositive("commandTimeoutSeconds", f.CommandTimeoutSeconds)
	requirePositive("labelCap", f.LabelCap)

	if len(f.Queues) == 0 {
		add("queues must name at least one queue")
	}
	var classified []queue.Queue
	seenQ := map[string]bool{}
	for i, qs := range f.Queues {
		if !queueNameRE.MatchString(qs.Name) {
			add("queues[%d].name %q is not a valid queue name", i, qs.Name)
			continue
		}
		if seenQ[qs.Name] {
			add("queues[%d].name %q is duplicated", i, qs.Name)
			continue
		}
		seenQ[qs.Name] = true
		if qs.Args == nil {
			add("queues[%d] (%s): args is required (it may be an empty list)", i, qs.Name)
			continue
		}
		q, err := queue.New(qs.Name, qs.Args)
		if err != nil {
			add("queues[%d]: %v", i, err)
			continue
		}
		classified = append(classified, q)
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("invalid config:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return &Config{File: f, DBNames: names, ClassifiedQueues: classified}, nil
}
