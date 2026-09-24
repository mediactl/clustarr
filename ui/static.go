/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package ui

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
)

// staticFiles embeds ui/static's committed contents into the clustarr
// binary: ui/static/app.css, built ahead of time from ui/static/input.css
// by the standalone Tailwind CLI (Makefile's `css` target, Phase G ruling
// R4 -- no Node, no CDN, no client fetch to a server this binary is not
// running), the vendored htmx.min.js / htmx-ext-sse.js that
// ui/views/layout.templ links, and the Open Sans face the Plex theme names
// (ui/theme/plex.json): the latin and latin-ext variable woff2 subsets under
// static/fonts, self-hosted so a page never fetches its text face from a
// third party, with the OFL beside them. input.css itself is deliberately
// not embedded or served -- it is a build-time input, consumed only by
// `make css`, not a runtime asset.
//
//go:embed static/app.css static/htmx.min.js static/htmx-ext-sse.js static/jump.js static/fonts/*.woff2 static/js/*.js
var staticFiles embed.FS

// The distroless image has no /etc/mime.types, so Go's table would serve a
// woff2 as application/octet-stream and a strict browser would refuse it.
func init() {
	if err := mime.AddExtensionType(".woff2", "font/woff2"); err != nil {
		panic("ui: register woff2 media type: " + err.Error())
	}
}

// staticCacheControl is set on every response staticHandler serves. These
// assets change only when a new clustarr binary is deployed (they are
// embedded via go:embed, not read from disk), so a redeploy always
// invalidates the old bytes along with the old binary -- there is no way
// for a client to observe a stale asset next to a new one the way there
// would be for a file served from a writable volume. An hour is long
// enough to spare a browser from refetching htmx on every page navigation,
// short enough that a support session debugging "the UI looks wrong" is
// never stuck more than an hour behind a fix.
const staticCacheControl = "public, max-age=3600"

// staticHandler serves ui/static's embedded contents at the paths
// ui/routes.go mounts it under (today, "/static/"), setting
// [staticCacheControl] on every response. It panics if staticFiles does not
// contain a "static" directory, which can only happen if the go:embed
// directive above and this function's fs.Sub call disagree about that path
// -- a compile-time-checkable mistake, not a runtime condition a caller
// could hit by making a request.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("ui: static/ not found in embedded FS: " + err.Error())
	}
	fileServer := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", staticCacheControl)
		fileServer.ServeHTTP(w, r)
	})
}
