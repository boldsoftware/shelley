// Package embedjs serves embed.js, the script a web page includes to dock
// Shelley in a pane next to it:
//
//	<script src="https://shelley.example.com/embed.js" defer></script>
//
// Shelley serves it at /embed.js. A proxy that signs in Shelley's frames
// itself, as exe.dev's does, may serve it elsewhere: the script frames the
// front page of the origin it was loaded from, whatever its path.
package embedjs

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"time"
)

//go:embed embed.js
var script []byte

var etag = func() string {
	sum := sha256.Sum256(script)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
}()

// ServeHTTP serves embed.js. Browsers revalidate it on every load, so a
// page picks up a new version as soon as it is deployed.
func ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "embed.js", time.Time{}, bytes.NewReader(script))
}
