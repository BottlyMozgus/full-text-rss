// Package webassets embeds the admin UI templates and static files so the
// server binary ships with everything it needs. Templates live under
// templates/ (parsed with layout.html first so pages override its blocks),
// static assets are served verbatim at /static/.
package webassets

import "embed"

// TemplatesFS holds the *.html template files.
//
//go:embed templates/*.html
var TemplatesFS embed.FS

// StaticFS holds css/ and js/ served at /static/.
//
//go:embed static
var StaticFS embed.FS
