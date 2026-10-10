// Package web embeds the static assets the daemon serves to a browser. Until
// the web UI sub-project lands it is a placeholder page, so a browser that
// follows a deep link or the public URL finds the daemon instead of an error.
package web

import _ "embed"

//go:embed index.html
var index []byte

// Index returns the page served at the root. The caller owns the slice.
func Index() []byte { return append([]byte(nil), index...) }
