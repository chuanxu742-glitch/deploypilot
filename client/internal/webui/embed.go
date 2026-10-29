// Package webui embeds the built React frontend.
package webui

import (
	"embed"
	"io/fs"
)

// dist holds the production build produced by `npm run build` in ../web.
// Run the frontend build before building the Go binary, otherwise this
// directory only contains the checked-in placeholder.
//
//go:embed all:dist
var dist embed.FS

// FS returns the embedded dist directory rooted at its top level.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
