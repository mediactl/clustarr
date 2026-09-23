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

// Package musicbrainz wraps go.uploadedlobster.com/musicbrainzws2 as a
// metadata.ArtistProvider.
//
// The brief this task followed recommended writing MusicBrainz by hand
// (docs/research/metadata.md §2.3: "last commit 2018-10-12 -> write our
// own"), but Phase B's dependency pre-add list instead committed to
// musicbrainzws2 v0.19.0. Its exact call surface was independently
// re-verified with `go doc go.uploadedlobster.com/musicbrainzws2` and `go
// doc go.uploadedlobster.com/musicbrainzws2.Client` before writing this
// file (recorded in the task report): the constructor is
// NewWithHTTPClient(AppInfo, *http.Client), not the brief's sketched
// New(apiKey, ...); the user agent is a single opaque string set via
// Client.SetUserAgent, overriding AppInfo's own "Name/Version (URL)"
// generation, since this package's New takes one pre-formatted userAgent
// string rather than AppInfo's three fields; the base URL is overridden
// with Client.SetBaseURL; a lookup is LookupArtist(ctx, mbtypes.MBID,
// IncludesFilter); and a failed request surfaces as *musicbrainzws2.ClientError
// carrying the HTTP status code, not a typed per-status error.
//
// musicbrainzws2 flattens every non-HTTP failure into
// ClientError{StatusCode: 0, Message: err.Error()} (handleErrorResponse in
// its client.go), which loses the cause: a refused connection, a cancelled
// context, an oversized body and a malformed 200 all look alike. So every
// call carries a callOutcome in its context, and outcomeTransport -- the
// transport under the library's resty client -- records what the last
// round trip actually did. mapError reads that, not the flattened string.
// The same transport stack applies the response-size cap
// (metadata.CappedTransport): resty reads each body whole, so the cap has to
// sit beneath it.
package musicbrainz

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"go.uploadedlobster.com/mbtypes"
	mb "go.uploadedlobster.com/musicbrainzws2"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// artistIncludes are the sub-resources requested on every artist lookup:
// ratings and genres for Artist.Ratings/Genres, url-rels for Artist.Links
// (a relation whose target is a URL entity), tags for Artist.Tags.
var artistIncludes = []string{"ratings", "genres", "url-rels", "tags"}

// releaseIncludes are the sub-resources requested when browsing a release
// group's releases (docs/research/metadata.md §2.3's release inc list,
// re-verified against https://musicbrainz.org/doc/MusicBrainz_API: browse
// release accepts "artist-credits, labels, recordings, release-groups,
// media, discids, isrcs"): media and recordings for AlbumRelease.Media and
// its tracks, labels for Labels/CatalogNo, artist-credits for each track's
// ArtistCredit.
var releaseIncludes = []string{"artist-credits", "labels", "media", "recordings"}

// maxReleasePages bounds how many release-browse requests one Album call
// makes. MusicBrainz caps a browse page at 100 releases and, with
// inc=recordings, at as many releases as fit in 500 tracks ("we limit the
// number of releases returned such that the entire list contains no more
// than 500 tracks" -- MusicBrainz_API, Browse), so a popular album takes
// several pages; at MusicBrainz's 1 request/second this is also a bound on
// how long one Album call can take. Ten pages covers every release group
// short of the most-reissued few hundred.
const maxReleasePages = 10

// searchLimit is how many artists SearchArtists asks for: MusicBrainz's own
// default page size (musicbrainzws2.DefaultLimit), which is plenty for a
// title-resolution caller that takes the best-scored hit.
const searchLimit = mb.DefaultLimit

// Client is a metadata.ArtistProvider backed by MusicBrainz.
type Client struct {
	raw     *mb.Client
	limiter *rate.Limiter
}

// New builds a Client. userAgent must identify the application and a
// contact (MusicBrainz requires this -- an unidentified client is silently
// downgraded to the shared, heavily throttled anonymous bucket,
// docs/research/metadata.md §2.3); it is set verbatim via SetUserAgent, not
// derived from musicbrainzws2.AppInfo. httpClient may be nil, in which case
// http.DefaultTransport is used; either way its transport is wrapped in the
// response cap and the per-call outcome recorder (see the package doc), so
// the library always gets an *http.Client built here. baseURL overrides
// MusicBrainz's default host -- tests pass an httptest.Server URL;
// production callers pass "".
func New(userAgent string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) (*Client, error) {
	if userAgent == "" {
		return nil, fmt.Errorf("musicbrainz: userAgent is required")
	}

	base := http.DefaultTransport
	hc := &http.Client{}
	if httpClient != nil {
		if httpClient.Transport != nil {
			base = httpClient.Transport
		}
		hc.Timeout = httpClient.Timeout
		hc.Jar = httpClient.Jar
		hc.CheckRedirect = httpClient.CheckRedirect
	}
	hc.Transport = &outcomeTransport{base: metadata.CappedTransport(base, metadata.MaxResponseBytes)}

	raw := mb.NewWithHTTPClient(mb.AppInfo{}, hc)
	raw.SetUserAgent(userAgent)
	if baseURL != "" {
		raw.SetBaseURL(baseURL)
	}

	return &Client{raw: raw, limiter: limiter}, nil
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "musicbrainz" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyMBArtist, metadata.KeyMBReleaseGroup}, Search: true}
}

// SearchArtists searches MusicBrainz's artist index by name
// (GET /ws/2/artist?query=), returning the first page of hits in
// MusicBrainz's own score order. The query is sent with dismax=true, which
// makes MusicBrainz treat it as plain text rather than Lucene syntax
// (MusicBrainz_API/Search: dismax "will escape certain special query syntax
// characters by default for ease of use") -- q comes from an import list or
// a user, not from a query builder, so a band called "AC/DC" or "!!!" must
// not be parsed as operators. SearchHit.Year is the artist's life-span begin
// year, the only year an artist has.
func (c *Client) SearchArtists(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.musicbrainz.SearchArtists")
	defer span.End()
	logger := logging.FromContext(ctx)

	if err := c.limiter.Wait(ctx); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	ctx, outcome := withOutcome(ctx)
	result, err := c.raw.SearchArtists(ctx, mb.SearchFilter{Query: q, Dismax: true}, mb.Paginator{Limit: searchLimit})
	if err != nil {
		mapped := mapError(err, outcome)
		tracing.RecordError(span, mapped)
		logger.ErrorContext(ctx, "musicbrainz: artist search failed", "query", q, "error", mapped)
		return nil, mapped
	}

	hits := make([]metadata.SearchHit, 0, len(result.Artists))
	for _, a := range result.Artists {
		hit := metadata.SearchHit{
			IDs:   metadata.ExternalIDs{metadata.KeyMBArtist: string(a.ID)},
			Title: a.Name,
		}
		if t, ok := partialDateToTime(a.LifeSpan.Begin); ok {
			hit.Year = int32(t.Year())
		}
		hits = append(hits, hit)
	}
	logger.DebugContext(ctx, "musicbrainz: artist search", "query", q, "hits", len(hits))
	return hits, nil
}

// Artist fetches a single artist by MusicBrainz id (MBID), with ratings,
// genres, tags and URL relations.
func (c *Client) Artist(ctx context.Context, mbArtistID string) (*metadata.Artist, error) {
	ctx, span := tracing.Start(ctx, "metadata.musicbrainz.Artist")
	defer span.End()
	logger := logging.FromContext(ctx)

	if err := c.limiter.Wait(ctx); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	ctx, outcome := withOutcome(ctx)
	raw, err := c.raw.LookupArtist(ctx, mbtypes.MBID(mbArtistID), mb.IncludesFilter{Includes: artistIncludes})
	if err != nil {
		mapped := mapError(err, outcome)
		tracing.RecordError(span, mapped)
		logger.ErrorContext(ctx, "musicbrainz: artist fetch failed", "mbid", mbArtistID, "error", mapped)
		return nil, mapped
	}

	a := mapArtist(raw)
	logger.DebugContext(ctx, "musicbrainz: artist fetched", "mbid", mbArtistID, "name", a.Name)
	return a, nil
}

// Albums browses the release groups credited to mbArtistID.
func (c *Client) Albums(ctx context.Context, mbArtistID string) ([]metadata.Album, error) {
	ctx, span := tracing.Start(ctx, "metadata.musicbrainz.Albums")
	defer span.End()
	logger := logging.FromContext(ctx)

	if err := c.limiter.Wait(ctx); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	ctx, outcome := withOutcome(ctx)
	result, err := c.raw.BrowseReleaseGroups(ctx, mb.ReleaseGroupFilter{ArtistMBID: mbtypes.MBID(mbArtistID)}, mb.DefaultPaginator())
	if err != nil {
		mapped := mapError(err, outcome)
		tracing.RecordError(span, mapped)
		logger.ErrorContext(ctx, "musicbrainz: albums browse failed", "mbid", mbArtistID, "error", mapped)
		return nil, mapped
	}

	albums := make([]metadata.Album, 0, len(result.ReleaseGroups))
	for _, rg := range result.ReleaseGroups {
		albums = append(albums, mapAlbum(rg))
	}
	return albums, nil
}

// Album looks up a single release group by MBID, then browses its releases
// -- each with its media and their tracks -- into Album.Releases
// (release group -> releases -> media -> tracks, the chain an Album's
// status.tracks is built from). Releases keep MusicBrainz's browse order;
// choosing one is the caller's policy, not this client's.
//
// Albums (an artist's whole release-group list, used for fan-out) does not
// do this: it would cost one or more extra requests per release group at
// MusicBrainz's 1 request/second, for data the fan-out never reads.
//
// A failed release browse fails the whole call rather than returning an
// Album with no releases: an empty Releases would read downstream as "this
// album has no releases" and clear its track list, when the truth is that
// the fetch did not complete.
func (c *Client) Album(ctx context.Context, mbReleaseGroupID string) (*metadata.Album, error) {
	ctx, span := tracing.Start(ctx, "metadata.musicbrainz.Album")
	defer span.End()
	logger := logging.FromContext(ctx)

	if err := c.limiter.Wait(ctx); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	lookupCtx, outcome := withOutcome(ctx)
	raw, err := c.raw.LookupReleaseGroup(lookupCtx, mbtypes.MBID(mbReleaseGroupID), mb.IncludesFilter{Includes: []string{"ratings", "genres"}})
	if err != nil {
		mapped := mapError(err, outcome)
		tracing.RecordError(span, mapped)
		logger.ErrorContext(ctx, "musicbrainz: album fetch failed", "mbid", mbReleaseGroupID, "error", mapped)
		return nil, mapped
	}

	album := mapAlbum(raw)
	releases, err := c.releases(ctx, mbReleaseGroupID)
	if err != nil {
		tracing.RecordError(span, err)
		logger.ErrorContext(ctx, "musicbrainz: album releases browse failed", "mbid", mbReleaseGroupID, "error", err)
		return nil, err
	}
	album.Releases = releases
	logger.DebugContext(ctx, "musicbrainz: album fetched", "mbid", mbReleaseGroupID, "title", album.Title, "releases", len(releases))
	return &album, nil
}

// releases browses every release of a release group, page by page, up to
// maxReleasePages. Each page is its own request and draws its own limiter
// token. A release group with more releases than that many pages hold is
// returned truncated, with a warning: every release this call did fetch is
// still correct, and one refresh must not become an unbounded crawl.
func (c *Client) releases(ctx context.Context, mbReleaseGroupID string) ([]metadata.AlbumRelease, error) {
	logger := logging.FromContext(ctx)
	filter := mb.ReleaseFilter{ReleaseGroupMBID: mbtypes.MBID(mbReleaseGroupID), Includes: releaseIncludes}

	var out []metadata.AlbumRelease
	offset := 0
	for page := 0; page < maxReleasePages; page++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		pageCtx, outcome := withOutcome(ctx)
		result, err := c.raw.BrowseReleases(pageCtx, filter, mb.Paginator{Offset: offset, Limit: mb.MaxLimit})
		if err != nil {
			return nil, mapError(err, outcome)
		}
		for _, r := range result.Releases {
			out = append(out, mapRelease(r))
		}
		offset += len(result.Releases)
		if len(result.Releases) == 0 || offset >= result.Count {
			return out, nil
		}
	}
	logger.WarnContext(ctx, "musicbrainz: release list truncated", "mbid", mbReleaseGroupID,
		"fetched", len(out), "pages", maxReleasePages)
	return out, nil
}

var _ metadata.ArtistProvider = (*Client)(nil)

// mapArtist converts a raw musicbrainzws2.Artist into the normalized model.
func mapArtist(raw mb.Artist) *metadata.Artist {
	a := &metadata.Artist{
		IDs:            metadata.ExternalIDs{metadata.KeyMBArtist: string(raw.ID)},
		Name:           raw.Name,
		SortName:       raw.SortName,
		Disambiguation: raw.Disambiguation,
		Type:           raw.Type,
		Country:        string(raw.CountryCode),
	}
	if t, ok := partialDateToTime(raw.LifeSpan.Begin); ok {
		a.Begin = &t
	}
	if t, ok := partialDateToTime(raw.LifeSpan.End); ok {
		a.End = &t
	}
	for _, g := range raw.Genres {
		a.Genres = append(a.Genres, g.Name)
	}
	for _, tg := range raw.Tags {
		a.Tags = append(a.Tags, tg.Name)
	}
	for _, rel := range raw.Relations {
		if rel.URL != nil {
			a.Links = append(a.Links, metadata.Link{Type: rel.Type, URL: rel.URL.Resource})
		}
	}
	if raw.Rating.VotesCount > 0 {
		a.Ratings = metadata.Ratings{"mb": {
			Source:      "mb",
			ValueCentis: ratingToValueCentis(raw.Rating.Value),
			Votes:       int32(raw.Rating.VotesCount),
			Kind:        "user",
		}}
	}
	return a
}

// mapAlbum converts a raw musicbrainzws2.ReleaseGroup into the normalized
// model.
func mapAlbum(rg mb.ReleaseGroup) metadata.Album {
	album := metadata.Album{
		IDs:            metadata.ExternalIDs{metadata.KeyMBReleaseGroup: string(rg.ID)},
		Title:          rg.Title,
		Disambiguation: rg.Disambiguation,
		PrimaryType:    rg.PrimaryType,
		SecondaryTypes: rg.SecondaryTypes,
	}
	if t, ok := partialDateToTime(rg.FirstReleaseDate); ok {
		album.ReleaseDate = &t
	}
	for _, g := range rg.Genres {
		album.Genres = append(album.Genres, g.Name)
	}
	if rg.Rating.VotesCount > 0 {
		album.Ratings = metadata.Ratings{"mb": {
			Source:      "mb",
			ValueCentis: ratingToValueCentis(rg.Rating.Value),
			Votes:       int32(rg.Rating.VotesCount),
			Kind:        "user",
		}}
	}
	return album
}

// mapRelease converts a raw musicbrainzws2.Release (browsed with
// releaseIncludes) into the normalized model. Status is MusicBrainz's own
// string ("Official", "Promotion", ...), passed through unchanged like
// Album.SecondaryTypes; a vocabulary crosswalk is the consumer's. A track is
// keyed by its recording MBID (metadata.KeyMBRecording), which is what an
// Album's status.tracks is keyed by; its Title is the track's title as
// printed on this release, which can differ from the recording's.
func mapRelease(r mb.Release) metadata.AlbumRelease {
	rel := metadata.AlbumRelease{
		IDs:            metadata.ExternalIDs{metadata.KeyMBRelease: string(r.ID)},
		Title:          r.Title,
		Disambiguation: r.Disambiguation,
		Status:         r.Status,
	}
	if t, ok := partialDateToTime(r.Date); ok {
		rel.Date = &t
	}
	rel.Country = releaseCountries(r)
	seenLabel := map[string]bool{}
	for _, li := range r.LabelInfo {
		if name := li.Label.Name; name != "" && !seenLabel[name] {
			seenLabel[name] = true
			rel.Labels = append(rel.Labels, name)
		}
		if rel.CatalogNo == "" && li.CatalogNumber != "" && li.CatalogNumber != "[none]" {
			rel.CatalogNo = li.CatalogNumber
		}
	}
	for _, m := range r.Media {
		medium := metadata.Medium{
			Position:   int32(m.Position),
			Format:     m.Format,
			Name:       m.Title,
			TrackCount: int32(m.TrackCount),
		}
		for _, tr := range m.Tracks {
			track := metadata.Track{
				Title:        tr.Title,
				Position:     int32(tr.Position),
				MediumNumber: int32(m.Position),
				Duration:     tr.Length.Duration,
				ArtistCredit: tr.ArtistCredit.String(),
			}
			if tr.Recording.ID != "" {
				track.IDs = metadata.ExternalIDs{metadata.KeyMBRecording: string(tr.Recording.ID)}
			}
			if track.Duration == 0 {
				track.Duration = tr.Recording.Length.Duration
			}
			medium.Tracks = append(medium.Tracks, track)
		}
		rel.TrackCount += medium.TrackCount
		rel.Media = append(rel.Media, medium)
	}
	return rel
}

// releaseCountries lists the ISO 3166-1 countries a release was issued in:
// its own country first, then each release event's, without duplicates.
// "XW" (MusicBrainz's [Worldwide] pseudo-country) is kept -- it is a real
// value a caller filtering by country has to be able to see.
func releaseCountries(r mb.Release) []string {
	var out []string
	seen := map[string]bool{}
	add := func(code string) {
		if code != "" && !seen[code] {
			seen[code] = true
			out = append(out, code)
		}
	}
	add(string(r.CountryCode))
	for _, ev := range r.ReleaseEvents {
		for _, code := range ev.Area.ISO31661Codes {
			add(string(code))
		}
	}
	return out
}

// ratingToValueCentis normalizes MusicBrainz's native 0-5 rating scale onto
// the shared 0-10-scaled-by-100 convention documented on metadata.Rating:
// ×2 to reach /10, ×100 to reach centis.
func ratingToValueCentis(v float32) int32 {
	return int32(math.Round(float64(v) * 2 * 100))
}

// partialDateToTime converts a MusicBrainz partial date (year, optionally
// month and day) into a time.Time anchored at the first of any missing
// month/day, reporting ok=false for an empty partial date.
func partialDateToTime(d mbtypes.PartialDate) (time.Time, bool) {
	if d.IsEmpty() {
		return time.Time{}, false
	}
	month := d.Month
	if month == 0 {
		month = 1
	}
	day := d.Day
	if day == 0 {
		day = 1
	}
	return time.Date(d.Year, time.Month(month), day, 0, 0, 0, 0, time.UTC), true
}

// callOutcome records what the transport under musicbrainzws2 actually did
// on the last round trip of one call, because the library's own error
// cannot say (see the package doc). One is attached per call by withOutcome
// and filled in by outcomeTransport; resty retries within a call overwrite
// it, so it always describes the attempt whose error the library returned.
type callOutcome struct {
	mu           sync.Mutex
	transportErr error // the round trip failed before any response arrived
	status       int   // the response's HTTP status; 0 when there was none
}

func (o *callOutcome) record(resp *http.Response, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.transportErr = err
	o.status = 0
	if err == nil && resp != nil {
		o.status = resp.StatusCode
	}
}

func (o *callOutcome) snapshot() (status int, transportErr error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.status, o.transportErr
}

type outcomeKey struct{}

// withOutcome attaches a fresh callOutcome to ctx for one library call.
func withOutcome(ctx context.Context) (context.Context, *callOutcome) {
	o := &callOutcome{}
	return context.WithValue(ctx, outcomeKey{}, o), o
}

// outcomeTransport records each round trip's result into the callOutcome
// its request's context carries (resty builds every request with the ctx
// the library call was given). A request with none is passed through.
type outcomeTransport struct {
	base http.RoundTripper
}

func (t *outcomeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if o, ok := req.Context().Value(outcomeKey{}).(*callOutcome); ok {
		o.record(resp, err)
	}
	return resp, err
}

// mapError maps a musicbrainzws2 error onto metadata's sentinel errors.
// A *ClientError with a real HTTP status maps by that status. MusicBrainz
// answers a client that exceeds its rate limit with 503 (docs/research/
// metadata.md §2.3: "violators get 503"), so 503 is ErrRateLimited alongside
// 429; the library has already retried both before giving up.
//
// A StatusCode of 0 -- or an error that is not a *ClientError at all -- is
// the library's flattened "something other than an HTTP status" case
// (verified in musicbrainzws2@v0.19.0's client.go, handleErrorResponse),
// and the call's recorded outcome decides what it was:
//
//   - the round trip itself failed: a transport error (refused, reset,
//     timed out, the context cancelled) or a body over
//     metadata.MaxResponseBytes, surfaced as that cause -- never ErrDecode;
//   - a 2xx arrived and the library still failed: its JSON decode of that
//     body failed, which is ErrDecode;
//   - anything else is wrapped as-is.
//
// Before this distinction every StatusCode-0 error was ErrDecode, so an
// unreachable MusicBrainz was reported as a malformed response.
func mapError(err error, outcome *callOutcome) error {
	var clientErr *mb.ClientError
	if errors.As(err, &clientErr) && clientErr.StatusCode != 0 {
		switch clientErr.StatusCode {
		case http.StatusNotFound:
			return metadata.ErrNotFound
		case http.StatusUnauthorized, http.StatusForbidden:
			return metadata.ErrAuth
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			return &metadata.RateLimitedError{Provider: "musicbrainz"}
		default:
			return fmt.Errorf("musicbrainz: HTTP %d: %w", clientErr.StatusCode, err)
		}
	}

	status, transportErr := outcome.snapshot()
	switch {
	case transportErr != nil:
		return fmt.Errorf("musicbrainz: %w", transportErr)
	case status >= 200 && status < 300:
		return fmt.Errorf("musicbrainz: %w: %w", metadata.ErrDecode, err)
	default:
		return fmt.Errorf("musicbrainz: %w", err)
	}
}
