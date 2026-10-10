// Package api embeds the OpenAPI 3.1 document of the pg-task-focus HTTP API.
// The document is the contract; this package only carries it into the binary
// and the tests.
package api

import _ "embed"

//go:embed openapi.yaml
var openapi []byte

// OpenAPI returns the OpenAPI 3.1 document, as YAML. The caller owns the
// returned slice.
func OpenAPI() []byte { return append([]byte(nil), openapi...) }
