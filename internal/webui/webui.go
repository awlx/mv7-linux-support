// Package webui embeds the MV7+ console web interface.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:web
var files embed.FS

// FS returns the embedded web filesystem rooted at the web directory.
func FS() (fs.FS, error) {
	return fs.Sub(files, "web")
}
