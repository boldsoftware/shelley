//go:build noui

package ui

import (
	"embed"
	"net/http"
)

// Built with -tags noui, the server has no web UI: it answers its API and
// serves no pages. The build then needs none of the UI's output, so neither
// Node nor a UI build. For tests of other clients; see `make build-noui`.

// Dist is empty.
var Dist embed.FS

// Assets returns a filesystem with no files.
func Assets() http.FileSystem { return http.FS(Dist) }

// Checksums returns none.
func Checksums() map[string]string { return nil }

// BuildID is empty: there is no UI build to identify.
func BuildID() string { return "" }
