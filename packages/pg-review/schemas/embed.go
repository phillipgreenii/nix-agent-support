// Package schemas holds the pinned JSON Schema files of the three pg-review
// contracts, embedded in the binary. The files stay under this directory so
// they can be read and reviewed as files.
package schemas

import "embed"

// FS holds every *.schema.json file of this directory.
//
//go:embed *.schema.json
var FS embed.FS
