// Package golden loads the golden message fixtures that pg-router's
// conformance tests and the conformance driver compare against.
//
// It is a LEAF test-support package: it imports nothing from package
// conformance (so conformance's own tests can import it without a cycle) and
// no production binary imports it. Keeping the embedded fixtures here, instead
// of in package conformance, means test data is not a compile-time dependency
// of any production package.
package golden

import (
	"embed"
	"encoding/json"
)

//go:embed testdata/golden/*.json
var goldenFS embed.FS

// Golden loads and decodes the golden fixture for message type mt
// (testdata/golden/<mt>.json) as a JSON object. This is the one golden-loading
// implementation.
func Golden(mt string) (map[string]any, error) {
	b, err := goldenFS.ReadFile("testdata/golden/" + mt + ".json")
	if err != nil {
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}
