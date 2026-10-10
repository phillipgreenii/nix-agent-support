// Package web embeds the static assets the daemon serves to a browser: the
// page (index.html) and the native ES modules and stylesheet it loads from
// assets/. There is no build step, so what is embedded is what is written; the
// layout and the reasons are in docs/adr/0092 of the repository.
//
// The assets are one flat directory so a single route (GET /assets/{name})
// serves them, and Lookup answers only for the names that are embedded, so a
// request can never name a file outside them.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed index.html assets
var files embed.FS

// Asset is one embedded file, ready to serve.
type Asset struct {
	// Name is the file's name under /assets/ (or "index.html").
	Name string
	// ContentType is the media type to send, with its charset for text.
	ContentType string
	// Body is the file's bytes. It is shared: callers MUST NOT modify it.
	Body []byte
	// ETag is a strong validator derived from the content, quoted.
	ETag string
}

var (
	index  Asset
	assets = map[string]Asset{}
)

func init() {
	b, err := files.ReadFile("index.html")
	if err != nil {
		panic("web: index.html is not embedded: " + err.Error())
	}
	index = newAsset("index.html", b)
	entries, err := fs.ReadDir(files, "assets")
	if err != nil {
		panic("web: assets/ is not embedded: " + err.Error())
	}
	for _, e := range entries {
		if e.IsDir() {
			panic("web: assets/" + e.Name() + " is a directory; the assets are one flat directory")
		}
		body, err := files.ReadFile(path.Join("assets", e.Name()))
		if err != nil {
			panic("web: " + err.Error())
		}
		assets[e.Name()] = newAsset(e.Name(), body)
	}
}

func newAsset(name string, body []byte) Asset {
	sum := sha256.Sum256(body)
	return Asset{Name: name, ContentType: contentType(name), Body: body, ETag: `"` + hex.EncodeToString(sum[:8]) + `"`}
}

// contentType is the media type of a file by extension. The set is closed: an
// extension that is not here would be a file the page cannot use.
func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".mjs", ".js":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	return "application/octet-stream"
}

// Index returns the page served at the root.
func Index() Asset { return index }

// Lookup returns the asset with the given name under /assets/, if it is
// embedded.
func Lookup(name string) (Asset, bool) {
	a, ok := assets[name]
	return a, ok
}

// Names lists the embedded asset names, sorted.
func Names() []string {
	out := make([]string, 0, len(assets))
	for n := range assets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
