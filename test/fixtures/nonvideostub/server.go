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

// Package nonvideostub serves the non-video metadata providers
// pkg/metadata/clients has real clients for -- MusicBrainz, Open Library,
// Audnexus and ComicVine, and since the 2026-11-10 non-video libraries
// design (docs/superpowers/specs/2026-11-10-non-video-libraries-design.md
// §5.4) the Cover Art Archive, Wikidata plus Wikipedia's summary and
// Audible's catalog search -- from one process, re-serving the recorded JSON
// under test/data/metadata/{musicbrainz,openlibrary,audnexus,comicvine,
// coverart,wikipedia,audible}/. Every route below is read off each client's
// own request-building code (pkg/metadata/clients/<provider>/<provider>.go's
// GetJSON and doGet calls, or, for MusicBrainz, its own unit test's asserted
// request paths -- that client wraps go.uploadedlobster.com/musicbrainzws2,
// which this package does not import), never guessed.
//
// # One process, seven path prefixes
//
// Each client's own default base URL already differs (musicbrainz.org,
// openlibrary.org, api.audnex.us, comicvine.gamespot.com/api,
// coverartarchive.org, www.wikidata.org, api.audible.com), and
// MetadataProvider.spec.baseURL (app/catalog/metadata/registry.go's baseURL
// helper) is a full replacement, not a suffix -- so config/e2e can point
// each MetadataProvider at this ONE Service, differing only in path prefix:
// http://nonvideo-stub.clustarr-system.svc/{musicbrainz,openlibrary,
// audnexus,comicvine,coverart,wikipedia,audible}. This mirrors torznabstub's
// "three personalities, selected by path" shape, one level higher: seven
// real upstream APIs share a service instead of three failure modes of one.
//
// # Recorded and shaped fixtures
//
// Most files are recordings of the live API. Three are SHAPED after a
// recording rather than recorded, because the e2e scenario's items (Kid A,
// Radiohead) were recorded before the gateway asked these providers about
// them, and the e2e cluster has no egress to record them now:
// coverart/release-group_0b56cf2b-….json (Kid A's front cover, in the
// shape of the recorded release-group_1b022e01-….json, naming Kid A's own
// GB release), wikipedia/radiohead_wikidata.json (Q44190's enwiki sitelink,
// the shape of muse_wikidata.json) and wikipedia/radiohead_summary.json
// (the shape of muse_summary.json). Each is named here so nobody mistakes
// it for a recording.
//
// It never talks to the Internet; the e2e cluster has no egress.
package nonvideostub

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ComicVineAPIKey is the key this stub accepts on ComicVine's api_key query
// parameter. ComicVine is the one provider here that requires a secretRef
// (app/catalog/metadata/registry.go's ComicVine case); the other three need
// none, matching pkg/metadata/clients' own constructors.
const ComicVineAPIKey = "e2e-fixture-key"

// The MusicBrainz artists this stub answers for. Radiohead is scenario 11's
// artist (one release group, Kid A, so one browse page); Muse is the artist
// whose release-group browse the stub pages (175 release groups over two
// pages of 100, recorded as browse_releasegroups_muse_p1/p2.json), which is
// how pkg/metadata/clients/musicbrainz's Albums pages a real browse.
const (
	RadioheadMBID = "a74b1b7f-71a5-4011-9441-d0b5e4122711"
	MuseMBID      = "9c9f1380-2516-4fc9-a3e6-f9f61941d090"
	// KidAReleaseGroupMBID is the one release group the Radiohead browse
	// answers with, and the one the Cover Art Archive route has art for.
	KidAReleaseGroupMBID = "0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6"
	// MuseReleaseGroupCount is how many release groups the two Muse browse
	// pages hold together ("release-group-count" on both).
	MuseReleaseGroupCount = 175
	// museBrowsePageSize is the offset the second Muse page starts at:
	// musicbrainzws2's MaxLimit, which the client's Albums asks for.
	museBrowsePageSize = "100"
)

// The Wikidata entities and Wikipedia pages this stub answers for
// (pkg/metadata/clients/wikipedia: a wbgetentities lookup of the artist's
// wikidata url-rel, then the page summary of its enwiki sitelink).
const (
	RadioheadWikidataID = "Q44190" // artist_radiohead_members.json's wikidata url-rel
	MuseWikidataID      = "Q22151" // the recorded muse_wikidata.json
)

// AudibleRecordedTitle is the catalog search this stub has a recording
// for (search_left_hand.json: Ursula K. Le Guin's The Left Hand of
// Darkness, three products); any other title answers an empty catalog
// in Audible's own shape.
const AudibleRecordedTitle = "left hand"

// AudnexusAuthorASIN is the one Audnexus author this stub has recorded
// (author_B000AP9A6K.json): the route pkg/metadata/clients/audnexus's
// AudiobookAuthor asks, GET /authors/{asin}?region=.
const AudnexusAuthorASIN = "B000AP9A6K"

// mbidRE is the shape of a MusicBrainz id, which the Cover Art Archive
// routes take as a path value and turn into a fixture file name; anything
// else 404s rather than reaching the filesystem.
var mbidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// NewHandler builds the stub. recordedDir holds test/data/metadata's
// provider subdirectories, copied verbatim by images/Dockerfile.e2e-fixtures
// (it already COPYs the whole test/data/metadata tree for tmdbstub/tvdbstub);
// local `go run` callers point --recorded-dir at ../../testdata/metadata
// instead.
func NewHandler(recordedDir string, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// MusicBrainz: base "/musicbrainz". go.uploadedlobster.com/musicbrainzws2
	// requests are unauthenticated apart from a mandatory User-Agent (which
	// this stub does not enforce -- app/catalog/metadata's own client
	// construction is what supplies it, and enforcing it here would only
	// duplicate that client's own required-field check).
	//
	// The artist lookup answers the full recording (artist_radiohead_members
	// .json: genres, tags, aliases, annotation, url-rels including the
	// wikidata one, artist-rels) since the gateway asks for every one of
	// those sub-resources (musicbrainz.go's artistIncludes, 2026-11-10
	// design §5.4), which is what fills ArtistMetadata.genres, aliases,
	// links and begin, and what artist.nfo renders (§8.1). The trimmed
	// artist_radiohead.json stays for that client's own unit tests. The
	// query string (inc=...) does not affect net/http's ServeMux path match.
	mux.HandleFunc("GET /musicbrainz/artist/"+RadioheadMBID,
		serveFile(filepath.Join(recordedDir, "musicbrainz", "artist_radiohead_members.json"), logger))
	mux.HandleFunc("GET /musicbrainz/artist/"+MuseMBID,
		serveFile(filepath.Join(recordedDir, "musicbrainz", "artist_muse_annotation_aliases.json"), logger))
	mux.HandleFunc("GET /musicbrainz/release-group/", musicbrainzReleaseGroups(recordedDir, logger))
	// Client.Album (pkg/metadata/clients/musicbrainz/musicbrainz.go's
	// releases method) additionally browses every release of a release
	// group via go.uploadedlobster.com/musicbrainzws2's Client.
	// BrowseReleases, which hits "/release/" with a "release-group" query
	// parameter (musicbrainzws2's own filter.go: r.SetQueryParam(
	// "release-group", ...)) -- never guessed, read off that vendored
	// library's source the same way musicbrainzReleaseGroups' own doc
	// comment describes for the release-group routes.
	mux.HandleFunc("GET /musicbrainz/release/", musicbrainzReleases(recordedDir, logger))

	// Cover Art Archive: base "/coverart". pkg/metadata/clients/coverart's
	// Artwork asks GET /release-group/{mbid} (or /release/{mbid}) for the
	// index of a release group's images and takes the approved front cover
	// as the album's poster. A release group the archive has no art for is
	// a 404, which the client maps to metadata.ErrNotFound and the gateway
	// takes as "no cover" rather than a failure; this stub answers the same
	// for any id it has no file for.
	mux.HandleFunc("GET /coverart/release-group/{mbid}", coverArt(recordedDir, "release-group", logger))
	mux.HandleFunc("GET /coverart/release/{mbid}", coverArt(recordedDir, "release", logger))

	// Wikidata and Wikipedia: base "/wikipedia", one prefix for both hosts.
	// pkg/metadata/clients/wikipedia reads an artist's biography in two
	// requests: the wikidata url-rel's entity through
	// <WikidataBaseURL>/w/api.php?action=wbgetentities&ids=<Q>&props=sitelinks
	// &sitefilter=enwiki, then the sitelink's page through
	// <WikipediaBaseURL>/api/rest_v1/page/summary/<title>. Both are served
	// here so one MetadataProvider.spec.baseURL covers both hosts; the
	// registry today derives only the Wikidata base from spec.baseURL and
	// leaves the Wikipedia one at en.wikipedia.org (registry.go's
	// MetadataProviderWikipedia case), so in a cluster without egress the
	// summary half is reached only once that is routed through the same
	// base -- the stub is ready for it either way.
	mux.HandleFunc("GET /wikipedia/w/api.php", wikidataEntities(recordedDir, logger))
	mux.HandleFunc("GET /wikipedia/api/rest_v1/page/summary/{title}", wikipediaSummary(recordedDir, logger))

	// Audible: base "/audible". pkg/metadata/clients/audible's Products
	// (Add New's audiobook search, metadata.AudiobookSearcher) asks the
	// marketplace's GET /1.0/catalog/products?title=&num_results=&
	// products_sort_by=&response_groups=; Config.BaseURL replaces the
	// per-region host, which is how the e2e provider points here.
	mux.HandleFunc("GET /audible/1.0/catalog/products", audibleProducts(recordedDir, logger))

	// Open Library: base "/openlibrary".
	mux.HandleFunc("GET /openlibrary/isbn/9780141439518.json",
		serveFile(filepath.Join(recordedDir, "openlibrary", "isbn_9780141439518.json"), logger))
	mux.HandleFunc("GET /openlibrary/authors/OL21594A.json",
		serveFile(filepath.Join(recordedDir, "openlibrary", "author_OL21594A.json"), logger))
	mux.HandleFunc("GET /openlibrary/works/OL138052W.json",
		serveFile(filepath.Join(recordedDir, "openlibrary", "work_OL138052W.json"), logger))
	// Client.Book additionally fetches this work's editions (openlibrary.go:
	// "/works/"+workID+"/editions.json?limit="+editionsLimit) to fill
	// Book.Editions and derive FirstPublished from the earliest one -- the
	// query string does not affect net/http's ServeMux path match.
	mux.HandleFunc("GET /openlibrary/works/OL138052W/editions.json",
		serveFile(filepath.Join(recordedDir, "openlibrary", "editions_OL138052W.json"), logger))
	// One path, two callers: Client.Books lists an author's works as a
	// search for "author_key:<olid>"; SearchBooks is a title search.
	mux.HandleFunc("GET /openlibrary/search.json", func(w http.ResponseWriter, r *http.Request) {
		name := "search_pride_and_prejudice.json"
		if strings.HasPrefix(r.URL.Query().Get("q"), "author_key:") {
			name = "search_works_author_OL21594A.json"
		}
		serveFile(filepath.Join(recordedDir, "openlibrary", name), logger)(w, r)
	})

	// Audnexus: base "/audnexus". The author route is the one
	// pkg/metadata/clients/audnexus's AudiobookAuthor asks (GET
	// /authors/{asin}?region=), wired as the registry's AudiobookAuthors
	// provider (registry.go) since the 2026-11-10 design's D11.
	mux.HandleFunc("GET /audnexus/books/B0036I54I6",
		serveFile(filepath.Join(recordedDir, "audnexus", "book_B0036I54I6.json"), logger))
	mux.HandleFunc("GET /audnexus/books/B0036I54I6/chapters",
		serveFile(filepath.Join(recordedDir, "audnexus", "chapters_B0036I54I6.json"), logger))
	mux.HandleFunc("GET /audnexus/authors/"+AudnexusAuthorASIN,
		serveFile(filepath.Join(recordedDir, "audnexus", "author_"+AudnexusAuthorASIN+".json"), logger))

	// ComicVine: base "/comicvine". Every route requires api_key -- verified
	// against comicvine.go's doGet, which always appends it -- so a missing
	// or wrong key surfaces spec.secretRef plumbing bugs the same way
	// torznabstub's APIKey check does for Torznab.
	//
	// The volume route's pattern is deliberately the exact, literal path
	// "/comicvine/volume/4050-18257" -- net/http's ServeMux matches it only
	// when the request carries the full, prefixed guid, exactly as
	// ComicVine's real GET /volume/{guid} endpoint requires (docs/research/
	// metadata.md §2.5). A request for the bare numeric id
	// ("/comicvine/volume/18257") falls through to notFound below instead
	// of matching here -- do not widen this to a wildcard pattern that
	// would accept both shapes; that is exactly the blind spot G2-5's fake
	// provider had (comicvine.go's Volume/Issues shape defect) that let
	// Comic->Issue ship broken against the real API.
	//
	// The canonical path carries ComicVine's trailing slash
	// ("/volume/4050-18257/", which pkg/metadata/clients/comicvine requests
	// since gap fix Z4); {$} keeps the pattern exact rather than a subtree.
	// The unslashed form gets the 301 to it the real API answers, so a
	// client that drops the slash still works but pays the round trip.
	mux.HandleFunc("GET /comicvine/volume/4050-18257/{$}", comicVineGated(
		filepath.Join(recordedDir, "comicvine", "volume_18257.json"), logger))
	mux.HandleFunc("GET /comicvine/volume/4050-18257", func(w http.ResponseWriter, r *http.Request) {
		u := *r.URL
		u.Path += "/"
		http.Redirect(w, r, u.String(), http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /comicvine/search/", comicVineGated(
		filepath.Join(recordedDir, "comicvine", "search_batman.json"), logger))
	// The issues route cannot enforce its id shape via the mux pattern the
	// way the volume route does above -- filter=volume:{id} is a query
	// parameter, not part of the path -- so comicVineIssues checks it
	// explicitly: ComicVine's filter=volume:{id} syntax wants the bare
	// numeric id ("volume:18257"), never the prefixed guid
	// ("volume:4050-18257") the volume route above requires. A stub that
	// served this route for either shape would accept exactly the
	// mismatched pair G2-5's fake provider did, which is how "Comic.spec.
	// sourceID sent unchanged to both Volume and Issues" survived: one of
	// the two real ComicVine endpoints must reject that value, and a fixture
	// that ignores the filter's shape can never catch it.
	mux.HandleFunc("GET /comicvine/issues/", comicVineIssues(
		filepath.Join(recordedDir, "comicvine", "issues_volume_18257.json"), logger))

	mux.HandleFunc("/", notFound(logger))
	return mux
}

// musicbrainzReleaseGroups answers both the browse (?artist=<mbid>, list
// endpoint) and the single-lookup (/release-group/{mbid}) shapes -- both hit
// the SAME "/release-group/" prefix in musicbrainzws2's own client, verified
// against musicbrainz_test.go's own two request-path assertions
// ("/release-group/" with an artist query parameter for the browse, versus
// "/release-group/{id}" with no query for the lookup).
//
// The browse pages by offset the way MusicBrainz does and the client's
// Albums reads (musicbrainz.go: offset += len(page) until offset >= count,
// mb.MaxLimit a page): Radiohead's one release group is one page whatever
// the offset asked for below it; Muse's 175 are the recorded page at offset
// 0 and the one at 100, and any other offset is a 404 so a client that
// pages wrongly fails loudly instead of reading the first page twice.
func musicbrainzReleaseGroups(recordedDir string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/musicbrainz/release-group/" && q.Get("artist") == RadioheadMBID:
			serveFile(filepath.Join(recordedDir, "musicbrainz", "browse_releasegroups_radiohead.json"), logger)(w, r)
		case r.URL.Path == "/musicbrainz/release-group/" && q.Get("artist") == MuseMBID:
			switch q.Get("offset") {
			case "", "0":
				serveFile(filepath.Join(recordedDir, "musicbrainz", "browse_releasegroups_muse_p1.json"), logger)(w, r)
			case museBrowsePageSize:
				serveFile(filepath.Join(recordedDir, "musicbrainz", "browse_releasegroups_muse_p2.json"), logger)(w, r)
			default:
				logger.Warn("nonvideostub: musicbrainz release-group browse at an offset no page starts at",
					"artist", MuseMBID, "offset", q.Get("offset"))
				notFound(logger)(w, r)
			}
		case r.URL.Path == "/musicbrainz/release-group/"+KidAReleaseGroupMBID:
			serveFile(filepath.Join(recordedDir, "musicbrainz", "releasegroup_kid_a.json"), logger)(w, r)
		default:
			notFound(logger)(w, r)
		}
	}
}

// musicbrainzReleases answers the release-group browse-releases shape
// (?release-group=<mbid>) Client.Album's releases method drives -- see
// NewHandler's route comment above for where that query shape comes from.
// It serves Kid A's own releases (browse_releases_kid_a.json, recorded from
// the real release group and trimmed to one three-release page), not The
// Bends' it served until gap fix Z6.
func musicbrainzReleases(recordedDir string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/musicbrainz/release/" && r.URL.Query().Get("release-group") == KidAReleaseGroupMBID:
			serveFile(filepath.Join(recordedDir, "musicbrainz", "browse_releases_kid_a.json"), logger)(w, r)
		default:
			notFound(logger)(w, r)
		}
	}
}

// coverArt serves the Cover Art Archive's index for one release group or
// release: coverart/<kind>_<mbid>.json when the fixture exists, else the
// archive's own 404 (an entity it has no art for). The id is checked
// against mbidRE before it becomes a file name.
func coverArt(recordedDir, kind string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mbid := r.PathValue("mbid")
		if !mbidRE.MatchString(mbid) {
			notFound(logger)(w, r)
			return
		}
		path := filepath.Join(recordedDir, "coverart", kind+"_"+mbid+".json")
		if _, err := os.Stat(path); err != nil {
			logger.Info("nonvideostub: coverart has no art for this entity", "kind", kind, "mbid", mbid)
			http.Error(w, `{"error":"No cover art found.","help":"For usage, please see: https://musicbrainz.org/doc/Cover_Art_Archive/API"}`, http.StatusNotFound)
			return
		}
		serveFile(path, logger)(w, r)
	}
}

// wikidataEntities answers wbgetentities for the entities this stub holds a
// sitelink answer for, by the ids parameter; any other id answers the
// recorded "missing" shape (nosite_wikidata.json), which the client reads as
// metadata.ErrNotFound. Another action is unrecorded.
func wikidataEntities(recordedDir string, logger *slog.Logger) http.HandlerFunc {
	files := map[string]string{
		RadioheadWikidataID: "radiohead_wikidata.json",
		MuseWikidataID:      "muse_wikidata.json",
	}
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("action") != "wbgetentities" {
			notFound(logger)(w, r)
			return
		}
		name, ok := files[q.Get("ids")]
		if !ok {
			logger.Info("nonvideostub: wikidata entity not recorded; answering missing", "ids", q.Get("ids"))
			name = "nosite_wikidata.json"
		}
		serveFile(filepath.Join(recordedDir, "wikipedia", name), logger)(w, r)
	}
}

// wikipediaSummary answers the REST summary of the pages this stub holds,
// by title as the client sends it (spaces as underscores, path-escaped);
// any other title is Wikipedia's own 404 body (summary_404.json) with its
// status.
func wikipediaSummary(recordedDir string, logger *slog.Logger) http.HandlerFunc {
	files := map[string]string{
		"Radiohead":   "radiohead_summary.json",
		"Muse_(band)": "muse_summary.json",
	}
	return func(w http.ResponseWriter, r *http.Request) {
		title := r.PathValue("title")
		name, ok := files[title]
		if !ok {
			logger.Info("nonvideostub: wikipedia page not recorded", "title", title)
			b, err := os.ReadFile(filepath.Join(recordedDir, "wikipedia", "summary_404.json"))
			if err != nil {
				http.Error(w, "fixture not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(b)
			return
		}
		serveFile(filepath.Join(recordedDir, "wikipedia", name), logger)(w, r)
	}
}

// audibleProducts answers the catalog search: the recorded page for a
// title that names AudibleRecordedTitle, an empty catalog in Audible's own
// envelope for anything else, so a search for a title this stub never
// recorded reads as "no hits", which is what the live API answers for a
// title it does not carry.
func audibleProducts(recordedDir string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		title := strings.ToLower(r.URL.Query().Get("title"))
		if title == "" {
			notFound(logger)(w, r)
			return
		}
		if !strings.Contains(title, AudibleRecordedTitle) {
			logger.Info("nonvideostub: audible title not recorded; answering an empty catalog", "title", title)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"product_filters":[],"products":[],"total_results":0}`))
			return
		}
		serveFile(filepath.Join(recordedDir, "audible", "search_left_hand.json"), logger)(w, r)
	}
}

// comicVineGated requires api_key=ComicVineAPIKey before serving path.
func comicVineGated(path string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != ComicVineAPIKey {
			logger.Warn("nonvideostub: comicvine request with a missing or wrong api_key", "path", r.URL.Path)
			http.Error(w, `{"error":"Invalid API Key","status_code":100}`, http.StatusUnauthorized)
			return
		}
		serveFile(path, logger)(w, r)
	}
}

// comicVineIssuesFilter is the only filter value this stub has recorded
// data for: the bare numeric volume id, matching ComicVine's real
// filter=volume:{id} syntax (see NewHandler's comment on the issues route
// for why the mux pattern alone cannot enforce this the way the volume
// route's literal path does).
const comicVineIssuesFilter = "volume:18257"

// comicVineIssues gates on api_key like comicVineGated, then additionally
// rejects any filter value other than comicVineIssuesFilter -- most
// notably the prefixed guid form ("volume:4050-18257") a client that still
// had G2-5's shape defect would send. ComicVine's own filter parser reports
// a malformed filter as an HTTP 200 with a non-1 status_code rather than an
// HTTP error status (docs/research/metadata.md §2.5's status-envelope
// note), but this stub answers 400 instead: doGet only maps HTTP status
// today (the TODO on Client.Volume), so a 200-with-bad-body would be
// decoded as if it were a valid, empty issue list and the caller would
// never see a failure -- a 400 makes the shape mismatch loud instead of
// silently returning zero issues.
func comicVineIssues(path string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != ComicVineAPIKey {
			logger.Warn("nonvideostub: comicvine request with a missing or wrong api_key", "path", r.URL.Path)
			http.Error(w, `{"error":"Invalid API Key","status_code":100}`, http.StatusUnauthorized)
			return
		}
		if filter := r.URL.Query().Get("filter"); filter != comicVineIssuesFilter {
			logger.Warn("nonvideostub: comicvine issues request with the wrong filter shape",
				"filter", filter, "want", comicVineIssuesFilter)
			http.Error(w, `{"error":"Filter Error","status_code":102}`, http.StatusBadRequest)
			return
		}
		serveFile(path, logger)(w, r)
	}
}

// serveFile answers with the recorded JSON at path, or 404s (loudly
// logged) when the image was built without it -- mirroring tmdbstub's own
// serveFile contract exactly. It re-reads per request on purpose: a stub
// that picks up an edited fixture without a restart is easier to debug
// than one that caches.
func serveFile(path string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(path)
		if err != nil {
			logger.Error("nonvideostub: recorded fixture missing", "path", path, "error", err)
			http.Error(w, "fixture not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}
}

// notFound logs every unrecognised route.
func notFound(logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logger.Warn("nonvideostub: unrecorded route", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)
		http.Error(w, "no recorded fixture for this route", http.StatusNotFound)
	}
}
