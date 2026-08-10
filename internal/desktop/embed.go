package desktop

import (
	"embed"
	"io/fs"
)

//go:embed all:ui/dist
var uiFS embed.FS

func UIAssets() (fs.FS, error) {
	return fs.Sub(uiFS, "ui/dist")
}
