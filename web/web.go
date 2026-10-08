// Package web embeds all static assets and HTML templates into the binary.
package web

import "embed"

//go:embed templates static
var FS embed.FS
