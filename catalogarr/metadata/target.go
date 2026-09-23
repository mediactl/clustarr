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

package metadata

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
)

// errUnsupportedKind marks a MediaKind this worker does not fetch metadata
// for. Task G2-1 grew this switch to cover every kind with its own
// status.metadata and ConditionMetadataReady -- Movie, Series, Artist,
// Album, Author, Book, Audiobook and Comic. MediaKindEpisode and
// MediaKindIssue are the two deliberate exceptions, not a gap: neither kind
// has a status.metadata field or a MetadataReady condition (see
// api/catalog/v1alpha1's episode_types.go and issue_types.go, and
// pkg/pipeline/project.go's describeItem, which treats both as "always
// metadata-synced" because each is only created once its parent's own
// metadata sync has already run). Their fields come from their parent's fan
// out (ManagerCatalogarrSeries for Episode; ManagerCatalogarrFanout for
// Issue, task G2-2) directly, never from this worker.
var errUnsupportedKind = errors.New("metadata: unsupported media kind for the metadata worker")

// newTarget returns a zero-value object of the concrete type kind names,
// ready for client.Get.
func newTarget(kind commonv1.MediaKind) (client.Object, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return &catalogv1alpha1.Movie{}, nil
	case commonv1.MediaKindSeries:
		return &catalogv1alpha1.Series{}, nil
	case commonv1.MediaKindArtist:
		return &catalogv1alpha1.Artist{}, nil
	case commonv1.MediaKindAlbum:
		return &catalogv1alpha1.Album{}, nil
	case commonv1.MediaKindAuthor:
		return &catalogv1alpha1.Author{}, nil
	case commonv1.MediaKindBook:
		return &catalogv1alpha1.Book{}, nil
	case commonv1.MediaKindAudiobook:
		return &catalogv1alpha1.Audiobook{}, nil
	case commonv1.MediaKindComic:
		return &catalogv1alpha1.Comic{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", errUnsupportedKind, kind)
	}
}

// externalIDs extracts the id(s) the registry looks a target up by, from its
// spec (§4.2: Movie a TMDB id, Series a TVDB id, Artist a MusicBrainz MBID,
// Album a MusicBrainz release-group MBID, Author/Book an Open Library key,
// Audiobook an ASIN plus its Audible marketplace region, Comic its source
// id). Audiobook additionally sets ids["region"]: pkg/metadata.Registry.
// Lookup's signature is fixed at (kind, ExternalIDs), so the region --
// unlike every other kind here, Audiobook needs a second, non-id parameter
// -- travels through the map the same way rpc.go's lookupEpisodes threads
// ids["order"] through to SeriesProvider.Episodes.
func externalIDs(obj client.Object) (pkgmetadata.ExternalIDs, error) {
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: strconv.FormatInt(o.Spec.TmdbID, 10)}, nil
	case *catalogv1alpha1.Series:
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: strconv.FormatInt(o.Spec.TvdbID, 10)}, nil
	case *catalogv1alpha1.Artist:
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyMBArtist: o.Spec.MusicBrainzID}, nil
	case *catalogv1alpha1.Album:
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: o.Spec.ReleaseGroupID}, nil
	case *catalogv1alpha1.Author:
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryAuthor: o.Spec.OpenLibraryID}, nil
	case *catalogv1alpha1.Book:
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyOpenLibraryWork: o.Spec.WorkID}, nil
	case *catalogv1alpha1.Audiobook:
		region := string(o.Spec.Region)
		if region == "" {
			region = "us"
		}
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyASIN: o.Spec.ASIN, "region": region}, nil
	case *catalogv1alpha1.Comic:
		// The id goes under its source's own key. ComicSpec.Source names the
		// provider SourceID belongs to, so this is a lookup, not a guess:
		// filing a MangaDex UUID under "comicvine", as this once did, handed
		// it to every ComicProvider as if it were a ComicVine volume.
		if o.Spec.Source == catalogv1alpha1.ComicSourceMangaDex {
			return pkgmetadata.ExternalIDs{extid.KeyMangaDex: o.Spec.SourceID}, nil
		}
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyComicVine: o.Spec.SourceID}, nil
	default:
		return nil, fmt.Errorf("%w: %T", errUnsupportedKind, obj)
	}
}

// refreshedAt returns the target's previous status.metadata.refreshedAt, or
// the zero time when it has never been fetched -- the "lastRefreshed"
// pkg/metadata.RefreshTTL needs.
func refreshedAt(obj client.Object) time.Time {
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		if o.Status.Metadata != nil {
			return o.Status.Metadata.RefreshedAt.Time
		}
	case *catalogv1alpha1.Series:
		if o.Status.Metadata != nil {
			return o.Status.Metadata.RefreshedAt.Time
		}
	case *catalogv1alpha1.Artist:
		if o.Status.Metadata != nil {
			return o.Status.Metadata.RefreshedAt.Time
		}
	case *catalogv1alpha1.Album:
		if o.Status.Metadata != nil {
			return o.Status.Metadata.RefreshedAt.Time
		}
	case *catalogv1alpha1.Author:
		if o.Status.Metadata != nil {
			return o.Status.Metadata.RefreshedAt.Time
		}
	case *catalogv1alpha1.Book:
		if o.Status.Metadata != nil {
			return o.Status.Metadata.RefreshedAt.Time
		}
	case *catalogv1alpha1.Audiobook:
		if o.Status.Metadata != nil {
			return o.Status.Metadata.RefreshedAt.Time
		}
	case *catalogv1alpha1.Comic:
		if o.Status.Metadata != nil {
			return o.Status.Metadata.RefreshedAt.Time
		}
	}
	return time.Time{}
}
