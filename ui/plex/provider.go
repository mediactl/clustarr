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

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
)

// IndexFunc returns the current [projection.Index] this package's routes
// look up Movie, Series and Episode objects through. ui/routes.go wires it
// to a projection.IndexMemo over Options.Reader, so every request inside
// projection.IndexTTL shares one build (the Index is read-only); a test
// wires it to projection.BuildIndex over a fake client.
type IndexFunc func(context.Context) (*projection.Index, error)

// Options configures [Handler].
type Options struct {
	// ExternalURL is the absolute base every thumb, art and Image[].url is
	// built on (spec §D.1) -- ui.Options.Plex.ExternalURL, threaded through
	// unchanged. Empty means `--external-url` was never set: [Handler]'s
	// root routes answer 503 rather than publish a relative URL Plex could
	// never fetch (ruling: spec §D.1, "the roots return 503").
	ExternalURL string

	// Index returns the current catalog lookup every non-root route reads
	// through.
	Index IndexFunc
}

// Handler returns the composed HTTP handler for both provider roots,
// /plex/movies (type 1, spec §D.1's movie provider) and /plex/tv (types 2,
// 3 and 4, the tv provider) -- the six routes of spec §D.2 under each,
// mounted on their absolute paths so ui/routes.go can register this
// unchanged at "/plex/" (the request path reaches this handler exactly as
// the browser -- or here, Plex Media Server -- sent it; there is no
// http.StripPrefix between them, unlike GET /static/).
func Handler(opts Options) http.Handler {
	h := &handler{opts: opts}
	mux := http.NewServeMux()
	h.register(mux, moviesRoot)
	h.register(mux, tvRoot)
	return mux
}

// handler closes every route over the [Options] it was built with.
type handler struct {
	opts Options
}

// rootDef is one provider root: its mount path, its own identifier/scheme,
// the numeric provider Types it declares (spec §D.1's table) and whether it
// answers the tv-only /children and /grandchildren routes.
type rootDef struct {
	path       string
	title      string
	identifier string
	types      []int
	tv         bool
}

var moviesRoot = rootDef{
	path:       "/plex/movies",
	title:      "Clustarr Movies",
	identifier: MoviesIdentifier,
	types:      []int{typeMovie},
}

var tvRoot = rootDef{
	path:       "/plex/tv",
	title:      "Clustarr TV",
	identifier: TVIdentifier,
	types:      []int{typeShow, typeSeason, typeEpisode},
	tv:         true,
}

// declares reports whether the root declares the numeric provider type t
// (spec §D.1's table): 1 on the movies root; 2, 3 and 4 on the tv root.
// Every route resolves only what its root declares -- a ratingKey or a
// match request naming another root's type is not found here -- so the
// movies provider never hands Plex a show, nor the tv provider a movie,
// under its own identifier.
func (r rootDef) declares(t int) bool {
	return slices.Contains(r.types, t)
}

// register wires one root's six routes (spec §D.2) onto mux.
func (h *handler) register(mux *http.ServeMux, root rootDef) {
	mux.HandleFunc("GET "+root.path, h.handleRoot(root))
	mux.HandleFunc("POST "+root.path+"/library/metadata/matches", h.handleMatch(root))
	mux.HandleFunc("GET "+root.path+"/library/metadata/{ratingKey}", h.handleMetadata(root))
	mux.HandleFunc("GET "+root.path+"/library/metadata/{ratingKey}/images", h.handleImages(root))
	if root.tv {
		mux.HandleFunc("GET "+root.path+"/library/metadata/{ratingKey}/children", h.handleChildren(root))
		mux.HandleFunc("GET "+root.path+"/library/metadata/{ratingKey}/grandchildren", h.handleGrandchildren(root))
	}
}

// index calls Options.Index, logging and answering 500 on failure -- the
// same "a read seam failed, tell the caller" shape every route in this
// package shares, since a Plex-facing 500 is exactly return-code table's own
// "internal error" (research §3).
func (h *handler) index(w http.ResponseWriter, r *http.Request) (*projection.Index, bool) {
	if h.opts.Index == nil {
		return &projection.Index{}, true
	}
	idx, err := h.opts.Index(r.Context())
	if err != nil {
		logging.FromContext(r.Context()).Error("plex: build catalog index", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to read the catalog")
		return nil, false
	}
	return idx, true
}

// writeJSON writes v as the whole response body: Content-Type
// application/json and Cache-Control no-store on every response (spec
// §D.2), whatever the route.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errorBody is the JSON shape of every non-2xx response this package
// answers with, e.g. §D.1's 503: {"error":"--external-url is required for
// the Plex provider"}.
type errorBody struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody{Error: message})
}

// MediaProvider is the root response's own object (research §2).
type MediaProvider struct {
	Identifier string         `json:"identifier"`
	Title      string         `json:"title"`
	Version    string         `json:"version,omitempty"`
	Types      []ProviderType `json:"Types"`
	Feature    []Feature      `json:"Feature"`
}

// ProviderType is one entry of MediaProvider.Types.
type ProviderType struct {
	Type   int      `json:"type"`
	Scheme []Scheme `json:"Scheme"`
}

// Scheme is one entry of a ProviderType's own Scheme array.
type Scheme struct {
	Scheme string `json:"scheme"`
}

// Feature is one entry of MediaProvider.Feature.
type Feature struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

// mediaProviderResponse is GET {root}'s whole response body.
type mediaProviderResponse struct {
	MediaProvider MediaProvider `json:"MediaProvider"`
}

// MetadataContainer is the response body's own MediaContainer object for
// every route that answers with Metadata objects: match, metadata, children
// and grandchildren (research §3). offset/totalSize/size are paging.go's
// own. Metadata carries no `omitempty` -- every caller assigns it a non-nil,
// possibly empty slice ([nonNilMetadata]) -- so a zero-result response is
// "Metadata":[], never the key missing or (a bare nil slice's own JSON
// form) "Metadata":null; spec §D.2's paging rule and D.4's "no match
// returns an empty container" both depend on that literal shape.
type MetadataContainer struct {
	Offset     int        `json:"offset"`
	TotalSize  int        `json:"totalSize"`
	Identifier string     `json:"identifier"`
	Size       int        `json:"size"`
	Metadata   []Metadata `json:"Metadata"`
}

// metadataContainerResponse is the whole response body for match, metadata,
// children and grandchildren.
type metadataContainerResponse struct {
	MediaContainer MetadataContainer `json:"MediaContainer"`
}

// ImageContainer is the /images route's own MediaContainer shape (research
// §7): an Image array where the routes above carry Metadata. Image also
// carries no `omitempty`, for the same reason.
type ImageContainer struct {
	Offset     int     `json:"offset"`
	TotalSize  int     `json:"totalSize"`
	Identifier string  `json:"identifier"`
	Size       int     `json:"size"`
	Image      []Image `json:"Image"`
}

// imageContainerResponse is the whole response body for /images.
type imageContainerResponse struct {
	MediaContainer ImageContainer `json:"MediaContainer"`
}

// nonNilMetadata turns a nil slice into a non-nil, empty one, so it
// marshals as [] rather than null through a field with no `omitempty`
// (encoding/json's own rule: only a non-nil, zero-length slice marshals to
// "[]"; a nil one marshals to "null" regardless of the tag).
func nonNilMetadata(items []Metadata) []Metadata {
	if items == nil {
		return []Metadata{}
	}
	return items
}

// nonNilImages is [nonNilMetadata] for an Image slice.
func nonNilImages(items []Image) []Image {
	if items == nil {
		return []Image{}
	}
	return items
}
