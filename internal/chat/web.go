package chat

import (
	"embed"
	"io/fs"
)

//go:embed web
var webFiles embed.FS

// webApp is the browser app in web/: plain ES modules, no build step.
var webApp, _ = fs.Sub(webFiles, "web")
