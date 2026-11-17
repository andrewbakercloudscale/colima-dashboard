// Package web holds the page, embedded into the binary.
package web

import "embed"

// Assets is the page: index.html, locked.html and the files under /static/.
//
//go:embed index.html locked.html app.js charts.js locked.js app.css icon.svg
var Assets embed.FS
