// Package web holds the admin UI, embedded into the sphynx binary.
package web

import "embed"

//go:embed index.html app.js style.css icon.svg
var Files embed.FS
