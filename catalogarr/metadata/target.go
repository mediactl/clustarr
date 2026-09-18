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
)

// errUnsupportedKind marks a MediaKind this M1 worker does not yet fetch
// metadata for. catalogarr/run.go's own setupControllers comment carries
// the matching TODO(M6): artist, album, author, book, audiobook, comic,
// issue -- this switch grows a case, not a redesign, when that lands.
var errUnsupportedKind = errors.New("metadata: unsupported media kind for the metadata worker")

// newTarget returns a zero-value object of the concrete type kind names,
// ready for client.Get.
func newTarget(kind commonv1.MediaKind) (client.Object, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return &catalogv1alpha1.Movie{}, nil
	case commonv1.MediaKindSeries:
		return &catalogv1alpha1.Series{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", errUnsupportedKind, kind)
	}
}

// externalIDs extracts the id the registry looks a target up by, from its
// spec (§4.2: Movie carries a TMDB id, Series a TVDB id).
func externalIDs(obj client.Object) (pkgmetadata.ExternalIDs, error) {
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: strconv.FormatInt(o.Spec.TmdbID, 10)}, nil
	case *catalogv1alpha1.Series:
		return pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: strconv.FormatInt(o.Spec.TvdbID, 10)}, nil
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
	}
	return time.Time{}
}

// movieRefreshState derives the RefreshTTL state bucket from a fetched
// Movie's own fields -- RefreshTTL does not know about CRDs or providers,
// only the state string the caller hands it.
func movieRefreshState(m *pkgmetadata.Movie, now time.Time) string {
	switch m.Status {
	case pkgmetadata.MovieStatusAnnounced:
		return pkgmetadata.RefreshStateAnnounced
	case pkgmetadata.MovieStatusInCinemas:
		return pkgmetadata.RefreshStateInCinemas
	case pkgmetadata.MovieStatusReleased:
		if latest := latestOf(m.DigitalRelease, m.PhysicalRelease); latest != nil && now.Sub(*latest) < 30*24*time.Hour {
			return pkgmetadata.RefreshStateReleasedRecent
		}
		return pkgmetadata.RefreshStateReleasedOld
	default:
		return pkgmetadata.RefreshStateReleasedOld
	}
}

func latestOf(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.After(*a):
		return b
	default:
		return a
	}
}

// seriesRefreshState is movieRefreshState's counterpart for Series.
func seriesRefreshState(s *pkgmetadata.Series, now time.Time) string {
	switch s.Status {
	case pkgmetadata.SeriesStatusContinuing:
		return pkgmetadata.RefreshStateContinuing
	case pkgmetadata.SeriesStatusEnded:
		if s.LastAired != nil && now.Sub(*s.LastAired) < 30*24*time.Hour {
			return pkgmetadata.RefreshStateEndedRecent
		}
		return pkgmetadata.RefreshStateEndedOld
	default: // upcoming
		return pkgmetadata.RefreshStateAnnounced
	}
}
