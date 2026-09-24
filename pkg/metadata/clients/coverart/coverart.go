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

// Package coverart is a metadata.ArtworkProvider for the Cover Art Archive
// (https://coverartarchive.org), the artwork store MusicBrainz releases
// and release groups point at (docs/research/metadata.md §2.3).
//
// The CAA serves a JSON index per release and per release group --
// GET /release/{mbid} and GET /release-group/{mbid}, each answering with a
// 307 to archive.org that net/http follows -- shaped
//
//	{"images":[{"approved":true,"front":true,"back":false,"types":["Front"],
//	  "image":"http://coverartarchive.org/release/<mbid>/<id>.jpg",
//	  "thumbnails":{"250":...,"500":...,"1200":...,"small":...,"large":...},
//	  "comment":"","edit":83020150,"id":30501372565}],
//	 "release":"https://musicbrainz.org/release/<mbid>"}
//
// (verified against the live service on 2026-09-23; the fixtures under
// test/data/metadata/coverart are those responses, unedited). A release or
// release group with no art answers 404, which maps to ErrNotFound.
//
// pkg/metadata/clients/musicbrainz.CoverArtURL builds the front-cover URL
// convention without a request; this client reads the index, which is the
// only way to learn which images exist and what each one is.
package coverart

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/time/rate"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/httpjson"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultBaseURL is the Cover Art Archive's API root.
const DefaultBaseURL = "https://coverartarchive.org"

// DefaultRate and DefaultBurst are the limit a caller should give this
// client when its MetadataProvider sets none. The CAA publishes no rate
// limit ("no rate limiting rules at CAA", docs/research/metadata.md §2.3);
// one request a second with a small burst is chosen politeness, not a
// documented number. The caller owns the limiter (CLAUDE.md) -- New never
// installs one of its own.
const (
	DefaultRate  rate.Limit = 1
	DefaultBurst int        = 2
)

// ErrInvalidMBID is returned, before any request is made, for an id that is
// not a MusicBrainz UUID. It keeps a malformed value from being spliced
// into the request path.
var ErrInvalidMBID = errors.New("coverart: not a MusicBrainz id")

var mbidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// probeReleaseGroup is Nirvana's "Nevermind", a release group the CAA has
// held art for since its launch. Ping only needs the service to answer, so
// a 404 would do as well; a real id keeps the probe honest.
const probeReleaseGroup = "1b022e01-4da6-387b-8658-8678046e4cef"

// Config configures a Client.
type Config struct {
	// HTTPClient sends the requests; nil means http.DefaultClient.
	HTTPClient *http.Client
	// BaseURL overrides DefaultBaseURL (tests, a mirror).
	BaseURL string
	// Limiter is waited on before every request; nil means no client-side
	// limit.
	Limiter *rate.Limiter
	// UserAgent overrides httpjson.DefaultUserAgent.
	UserAgent string
}

// Client is a metadata.ArtworkProvider backed by the Cover Art Archive.
type Client struct {
	h       *httpjson.Client
	baseURL string
}

// New builds a Client.
func New(cfg Config) *Client {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		h:       &httpjson.Client{Provider: "coverart", HTTP: cfg.HTTPClient, Limiter: cfg.Limiter, UserAgent: cfg.UserAgent},
		baseURL: base,
	}
}

// Name identifies this provider for logging and metrics.
func (c *Client) Name() string { return "coverart" }

// Capabilities describes what this provider supports.
func (c *Client) Capabilities() metadata.Capabilities {
	return metadata.Capabilities{LookupBy: []string{metadata.KeyMBReleaseGroup, metadata.KeyMBRelease}, Artwork: true}
}

type index struct {
	Images []struct {
		Approved bool     `json:"approved"`
		Front    bool     `json:"front"`
		Back     bool     `json:"back"`
		Types    []string `json:"types"`
		Image    string   `json:"image"`
	} `json:"images"`
}

// Artwork returns the approved cover art for an album. kind must be
// MediaKindAlbum: the CAA holds art only for releases and release groups.
// ids[KeyMBReleaseGroup] is preferred -- it is what an Album is keyed by
// -- and ids[KeyMBRelease] is used when only a release is known.
//
// Only two CAA image types map onto a metadata.ImageType without a guess:
// the primary front cover ("front": true) is the album's poster, as the
// Audnexus and Open Library clients already treat a cover, and a "Medium"
// image is the disc. A back cover, booklet, tray, spine, obi or liner has
// no ImageType and is left out rather than filed under the nearest
// neighbour. An image the CAA has not yet approved is left out too.
func (c *Client) Artwork(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) ([]metadata.Image, error) {
	ctx, span := tracing.Start(ctx, "metadata.coverart.Artwork")
	defer span.End()

	if kind != commonv1.MediaKindAlbum {
		err := fmt.Errorf("coverart: no artwork for kind %q: %w", kind, metadata.ErrUnsupported)
		tracing.RecordError(span, err)
		return nil, err
	}
	var path string
	switch {
	case ids[metadata.KeyMBReleaseGroup] != "":
		id := ids[metadata.KeyMBReleaseGroup]
		if !mbidPattern.MatchString(id) {
			err := fmt.Errorf("%w: %q", ErrInvalidMBID, id)
			tracing.RecordError(span, err)
			return nil, err
		}
		path = "/release-group/" + id
	case ids[metadata.KeyMBRelease] != "":
		id := ids[metadata.KeyMBRelease]
		if !mbidPattern.MatchString(id) {
			err := fmt.Errorf("%w: %q", ErrInvalidMBID, id)
			tracing.RecordError(span, err)
			return nil, err
		}
		path = "/release/" + id
	default:
		err := fmt.Errorf("coverart: Artwork needs %q or %q in ExternalIDs: %w", metadata.KeyMBReleaseGroup, metadata.KeyMBRelease, metadata.ErrUnsupported)
		tracing.RecordError(span, err)
		return nil, err
	}

	var raw index
	if err := c.h.GetJSON(ctx, c.baseURL+path, nil, &raw); err != nil {
		tracing.RecordError(span, err)
		logging.FromContext(ctx).DebugContext(ctx, "coverart: index fetch failed", "path", path, "error", err)
		return nil, err
	}

	var out []metadata.Image
	for _, img := range raw.Images {
		if !img.Approved || img.Image == "" {
			continue
		}
		var t metadata.ImageType
		switch {
		case img.Front:
			t = metadata.ImageTypePoster
		case slices.Contains(img.Types, "Medium"):
			t = metadata.ImageTypeDisc
		default:
			continue
		}
		out = append(out, metadata.Image{Type: t, URL: httpsURL(img.Image)})
	}
	return out, nil
}

// Ping proves the Cover Art Archive answers. A 404 is an answer: the probe
// asks about reachability, not about one release group's art.
func (c *Client) Ping(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "metadata.coverart.Ping")
	defer span.End()
	_, err := c.Artwork(ctx, commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBReleaseGroup: probeReleaseGroup})
	if err != nil && !errors.Is(err, metadata.ErrNotFound) {
		tracing.RecordError(span, err)
		return err
	}
	return nil
}

// httpsURL upgrades the plain-http image URLs the CAA's index still hands
// out ("image":"http://coverartarchive.org/...") to https, which the CAA
// serves on the same paths; anything else is returned unchanged. A page
// served over TLS would otherwise load every album cover as mixed content.
func httpsURL(u string) string {
	if rest, ok := strings.CutPrefix(u, "http://coverartarchive.org/"); ok {
		return "https://coverartarchive.org/" + rest
	}
	return u
}

var _ metadata.ArtworkProvider = (*Client)(nil)
