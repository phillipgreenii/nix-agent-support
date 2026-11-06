// Package schema validates JSON documents against the pinned pg-review
// contract schemas (evidence-row, evidence-rows, query-log-entry, findings).
package schema

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-review/schemas"
)

const (
	suffix = ".schema.json"
	// base is the URL space the embedded schemas live in; their $id values and
	// relative $refs resolve inside it and nothing is fetched.
	base = "https://schemas.pg-review.invalid/"
)

var (
	once     sync.Once
	compiler *jsonschema.Compiler
	initErr  error
	names    []string
)

type refuse struct{}

func (refuse) Load(url string) (any, error) {
	return nil, fmt.Errorf("resource %s is not available", url)
}

func load() {
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.UseLoader(refuse{})
	c.AssertFormat()
	entries, err := fs.ReadDir(schemas.FS, ".")
	if err != nil {
		initErr = err
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		b, err := fs.ReadFile(schemas.FS, e.Name())
		if err != nil {
			initErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			initErr = fmt.Errorf("%s: %w", e.Name(), err)
			return
		}
		if err := c.AddResource(base+e.Name(), doc); err != nil {
			initErr = fmt.Errorf("%s: %w", e.Name(), err)
			return
		}
		names = append(names, strings.TrimSuffix(e.Name(), suffix))
	}
	sort.Strings(names)
	compiler = c
}

// Names lists the pinned schema names (the file basename without
// .schema.json), sorted.
func Names() []string {
	once.Do(load)
	return append([]string(nil), names...)
}

// Validate checks doc (JSON bytes) against the named pinned schema. On failure
// the error names the failing JSON path as a JSON pointer ("/findings/0" or
// "" for the document root) and says what is wrong there.
func Validate(name string, doc []byte) error {
	once.Do(load)
	if initErr != nil {
		return fmt.Errorf("schema: loading pinned schemas: %w", initErr)
	}
	sch, err := compiler.Compile(base + name + suffix)
	if err != nil {
		return fmt.Errorf("schema: unknown or invalid schema %q: %w", name, err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return fmt.Errorf("document is not valid JSON: %w", err)
	}
	verr := sch.Validate(v)
	if verr == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if !errors.As(verr, &ve) {
		return verr
	}
	leaf := ve
	for len(leaf.Causes) > 0 {
		leaf = leaf.Causes[0]
	}
	path := ""
	for _, tok := range leaf.InstanceLocation {
		path += "/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(tok)
	}
	msg := leaf.ErrorKind.LocalizedString(message.NewPrinter(language.English))
	return fmt.Errorf("%s: invalid at path %q: %s", name, path, msg)
}
