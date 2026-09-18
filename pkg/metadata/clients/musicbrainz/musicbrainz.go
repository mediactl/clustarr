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
package musicbrainz

import (
	"context"
	"fmt"
	"math"
	"net/http"
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
// the library's own default (a fresh resty client over
// http.DefaultTransport) is used. baseURL overrides MusicBrainz's default
// host -- tests pass an httptest.Server URL; production callers pass "".
func New(userAgent string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) (*Client, error) {
	if userAgent == "" {
		return nil, fmt.Errorf("musicbrainz: userAgent is required")
	}

	var raw *mb.Client
	if httpClient != nil {
		raw = mb.NewWithHTTPClient(mb.AppInfo{}, httpClient)
	} else {
		raw = mb.NewClient(mb.AppInfo{})
	}
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
	return metadata.Capabilities{LookupBy: []string{metadata.KeyMBArtist}}
}

// SearchArtists is not implemented by this task: MusicBrainz search uses
// Lucene query syntax, which is out of scope here (see the brief). It
// returns metadata.ErrUnsupported until a later task needs it.
func (c *Client) SearchArtists(context.Context, string) ([]metadata.SearchHit, error) {
	return nil, metadata.ErrUnsupported
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

	raw, err := c.raw.LookupArtist(ctx, mbtypes.MBID(mbArtistID), mb.IncludesFilter{Includes: artistIncludes})
	if err != nil {
		mapped := mapError(err)
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

	result, err := c.raw.BrowseReleaseGroups(ctx, mb.ReleaseGroupFilter{ArtistMBID: mbtypes.MBID(mbArtistID)}, mb.DefaultPaginator())
	if err != nil {
		mapped := mapError(err)
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

// Album looks up a single release group by MBID.
func (c *Client) Album(ctx context.Context, mbReleaseGroupID string) (*metadata.Album, error) {
	ctx, span := tracing.Start(ctx, "metadata.musicbrainz.Album")
	defer span.End()
	logger := logging.FromContext(ctx)

	if err := c.limiter.Wait(ctx); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	raw, err := c.raw.LookupReleaseGroup(ctx, mbtypes.MBID(mbReleaseGroupID), mb.IncludesFilter{Includes: []string{"ratings", "genres"}})
	if err != nil {
		mapped := mapError(err)
		tracing.RecordError(span, mapped)
		logger.ErrorContext(ctx, "musicbrainz: album fetch failed", "mbid", mbReleaseGroupID, "error", mapped)
		return nil, mapped
	}

	album := mapAlbum(raw)
	return &album, nil
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

// mapError maps musicbrainzws2's *ClientError HTTP status onto metadata's
// sentinel errors.
func mapError(err error) error {
	var clientErr *mb.ClientError
	if !isClientError(err, &clientErr) {
		return fmt.Errorf("musicbrainz: %w", err)
	}
	switch clientErr.StatusCode {
	case http.StatusNotFound:
		return metadata.ErrNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return metadata.ErrAuth
	case http.StatusTooManyRequests:
		return &metadata.RateLimitedError{Provider: "musicbrainz"}
	default:
		return fmt.Errorf("musicbrainz: %w", err)
	}
}

// isClientError reports whether err is a *musicbrainzws2.ClientError,
// setting *target when it is. musicbrainzws2.ClientError does not
// implement Unwrap, so errors.As is not usable here; a direct type
// assertion is the correct check.
func isClientError(err error, target **mb.ClientError) bool {
	ce, ok := err.(*mb.ClientError)
	if ok {
		*target = ce
	}
	return ok
}
