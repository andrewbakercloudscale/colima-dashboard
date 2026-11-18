// Package web holds the page, embedded into the binary.
package web

import "embed"

// Assets is the page: index.html, locked.html and the files under /static/.
//
//go:embed index.html locked.html app.js report.js charts.js logfmt.js containers.js fix.js palette.js locked.js app.css icon.svg vendor/xterm.js vendor/xterm.css vendor/addon-fit.js vendor/XTERM-LICENSE
var Assets embed.FS
