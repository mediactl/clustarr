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

package plex

import "net/http"

// errExternalURLRequired is the 503 body's message (spec §D.1): "the roots
// return 503 with a body naming the flag". cmd/clustarr logs this same
// message once at startup (ui.NewServer) when --plex-provider is on with no
// --external-url, so an operator sees it before the first request ever
// does.
const errExternalURLRequired = "--external-url is required for the Plex provider"

// handleRoot answers GET {root}: the MediaProvider PMS reads once, at "Add
// Provider" time (research §2, §9.1), to learn this provider's identifier,
// declared Types and Feature keys. It is the one route spec §D.1 gates on
// ExternalURL: without it, no thumb/art/Image[].url this provider could ever
// hand back would resolve to anything Plex could fetch, and PMS never gets
// far enough to call match or metadata without first reading this
// successfully -- so gating here is enough to keep every other route from
// ever being reached with an unusable ExternalURL.
func (h *handler) handleRoot(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.opts.ExternalURL == "" {
			writeError(w, http.StatusServiceUnavailable, errExternalURLRequired)
			return
		}

		types := make([]ProviderType, len(root.types))
		for i, t := range root.types {
			types[i] = ProviderType{Type: t, Scheme: []Scheme{{Scheme: root.identifier}}}
		}

		writeJSON(w, http.StatusOK, mediaProviderResponse{MediaProvider: MediaProvider{
			Identifier: root.identifier,
			Title:      root.title,
			Types:      types,
			Feature: []Feature{
				{Type: "match", Key: "/library/metadata/matches"},
				{Type: "metadata", Key: "/library/metadata"},
			},
		}})
	}
}
