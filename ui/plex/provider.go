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

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
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

	// Extended reads an item's people and similar titles from the
	// clustarr-metadata-extended bucket (pkg/metadata/extended); ok is false
	// when the item has no document. Nil reads none. ui/ is handed this
	// closure, never a KV handle, so it cannot write (TestUINeverWrites).
	Extended func(ctx context.Context, kind commonv1.MediaKind, uid types.UID) (doc extended.Doc, ok bool, err error)

	// PhotoURL is the URL PMS is given for a provider-hosted image: the
	// image's own public URL, or "" for one it may not have (the ui's
	// plexPhotoURL). PMS keeps some such URLs, a person's photo above all,
	// and fetches them through images.plex.tv, which a cluster address
	// never reaches. Nil drops person photos, season posters and episode
	// stills.
	PhotoURL func(src string) string

	// PlexGUIDs answers a movie, show, season or episode Plex knows with
	// its plex:// GUID instead of clustarr's own (--plex-guids), so Plex
	// Web offers Watchlist and its other Discover features for it
	// (docs/superpowers/specs/2026-10-06-plex-native-guids-design.md).
	// ratingKey and key stay clustarr's either way.
	PlexGUIDs bool

	// Extras returns the extras Plex holds for a Plex id (PlexTVExtras), which
	// the extras route answers PMS with. Nil answers 503 for an item that
	// has a Plex id, never an empty list, which PMS would read as "none".
	Extras func(ctx context.Context, plexID string) ([]Extra, error)

	// ArtContentType is the content type of the artwork object /art serves
	// for an item's image of type t, "" when unknown. The URL of a WebP asks
	// /art for it in a format PMS decodes ([projection.ArtNoWebP]): PMS keeps
	// the bytes it downloads, and its UltraBlur processor decodes no WebP.
	// Nil asks for none.
	ArtContentType func(kind commonv1.MediaKind, uid types.UID, t catalogv1.ImageType) string

	// MusicProvider also serves the music and audiobook roots, /plex/music
	// and /plex/audiobooks (`--plex-music-provider`; 2026-11-10 design
	// §7.1, D12). Off by default: PMS through 1.43.5.11079 refuses a custom
	// provider declaring types 8, 9 or 10 ("The provider supports
	// unsupported metadata types", §7.6's as-run note), so the roots are
	// built to the inferred shapes and stay off until a build accepts
	// them. Index must then be built with projection.IndexOptions.Music,
	// else every music route answers "not found".
	MusicProvider bool
}

// Handler returns the composed HTTP handler for the provider roots,
// /plex/movies (type 1, spec §D.1's movie provider) and /plex/tv (types 2,
// 3 and 4, the tv provider) -- the six routes of spec §D.2 under each --
// and, under Options.MusicProvider, /plex/music and /plex/audiobooks
// (types 8, 9 and 10, design §7.1), mounted on their absolute paths so
// ui/routes.go can register this unchanged at "/plex/" (the request path
// reaches this handler exactly as the browser -- or here, Plex Media
// Server -- sent it; there is no http.StripPrefix between them, unlike
// GET /static/).
func Handler(opts Options) http.Handler {
	h := &handler{opts: opts}
	mux := http.NewServeMux()
	h.register(mux, moviesRoot)
	h.register(mux, tvRoot)
	if opts.MusicProvider {
		h.register(mux, musicRoot)
		h.register(mux, audiobooksRoot)
	}
	return mux
}

// handler closes every route over the [Options] it was built with.
type handler struct {
	opts Options
}

// rootDef is one provider root: its mount path, its own identifier/scheme,
// the numeric provider Types it declares (spec §D.1's table) and whether it
// answers the container routes, /children and /grandchildren (the tv root's
// show->seasons->episodes, the music roots' artist->albums->tracks).
type rootDef struct {
	path       string
	title      string
	identifier string
	types      []int
	tv         bool
	// music keys the music container routes as tv keys the tv ones
	// (design §7.1): the root serves artist, album and track objects.
	// audiobooks says which catalog kinds those are -- false: Artist, Album
	// and its tracks; true: Author (or a synthetic artist), Audiobook and
	// its parts -- so each music root serves only its own kinds
	// ([rootDef.serves]), as the movies root never hands Plex a show.
	music      bool
	audiobooks bool
	// collections serves the collection feature (collection.go). The
	// root does not declare type 18 for it: PMS 1.43.4 refuses a provider
	// that does ("The provider supports unsupported metadata types",
	// 2026-10-06), though Plex's provider docs list it.
	collections bool
}

var moviesRoot = rootDef{
	path:        "/plex/movies",
	title:       "Clustarr Movies",
	identifier:  MoviesIdentifier,
	types:       []int{typeMovie},
	collections: true,
}

var tvRoot = rootDef{
	path:       "/plex/tv",
	title:      "Clustarr TV",
	identifier: TVIdentifier,
	types:      []int{typeShow, typeSeason, typeEpisode},
	tv:         true,
}

// musicRoot and audiobooksRoot are the music roots (design §7.1): both
// declare Plex's music types 8 (artist), 9 (album) and 10 (track), since
// Plex has no audiobook library type and an audiobook library is a music
// library with Author as the artist, Audiobook as the album and each part
// as a track (§7.4). Neither declares collections: Plex supports them in
// movie libraries only. Registered only under Options.MusicProvider.
var musicRoot = rootDef{
	path:       "/plex/music",
	title:      "Clustarr Music",
	identifier: MusicIdentifier,
	types:      []int{typeArtist, typeAlbum, typeTrack},
	music:      true,
}

var audiobooksRoot = rootDef{
	path:       "/plex/audiobooks",
	title:      "Clustarr Audiobooks",
	identifier: AudiobooksIdentifier,
	types:      []int{typeArtist, typeAlbum, typeTrack},
	music:      true,
	audiobooks: true,
}

// declares reports whether the root declares the numeric provider type t
// (spec §D.1's table): 1 on the movies root; 2, 3 and 4 on the tv root; 8,
// 9 and 10 on the music roots. Every route resolves only what its root
// declares -- a ratingKey or a match request naming another root's type is
// not found here -- so the movies provider never hands Plex a show, nor
// the tv provider a movie, under its own identifier.
func (r rootDef) declares(t int) bool {
	return slices.Contains(r.types, t)
}

// serves reports whether the root serves the catalog object obj: a Movie
// on the movies root, a Series or Episode on the tv root ([rootDef.declares]
// of [providerType]), an Artist or Album on the music root and an Author
// or Audiobook on the audiobooks root alone -- both music roots declare
// the same numbers, so the type alone cannot tell them apart.
func (r rootDef) serves(obj any) bool {
	switch obj.(type) {
	case *catalogv1.Artist, *catalogv1.Album:
		return r.music && !r.audiobooks
	case *catalogv1.Author, *catalogv1.Audiobook:
		return r.audiobooks
	default:
		return r.declares(providerType(obj))
	}
}

// register wires one root's routes onto mux: spec §D.2's six, and the
// extras route PMS asks every provider for (extras.go).
func (h *handler) register(mux *http.ServeMux, root rootDef) {
	mux.HandleFunc("GET "+root.path, h.handleRoot(root))
	mux.HandleFunc("POST "+root.path+"/library/metadata/matches", h.handleMatch(root))
	mux.HandleFunc("GET "+root.path+"/library/metadata/{ratingKey}", h.handleMetadata(root))
	mux.HandleFunc("GET "+root.path+"/library/metadata/{ratingKey}/images", h.handleImages(root))
	mux.HandleFunc("GET "+root.path+"/library/metadata/{ratingKey}/extras", h.handleExtras(root))
	if root.collections {
		mux.HandleFunc("GET "+root.path+"/library/collections/{key}", h.handleCollection(root))
		mux.HandleFunc("GET "+root.path+"/library/collections/{key}/children", h.handleCollectionChildren(root))
	}
	if root.tv || root.music {
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
