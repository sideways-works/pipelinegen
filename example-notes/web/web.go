// Package web holds the notes page the server hands to browsers.
package web

import "embed"

// Files is the page and what it loads.
//
//go:embed index.html
var Files embed.FS
