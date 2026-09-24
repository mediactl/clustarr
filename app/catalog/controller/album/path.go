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

package album

import (
	"path"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

// ReleaseYear extracts the calendar year naming.Context.Year needs from an
// AlbumMetadata's ReleaseDate, or 0 when releaseDate is nil.
//
// .UTC() before .Year(), deliberately: metav1.Time.UnmarshalJSON
// (k8s.io/apimachinery) calls t.Local() on every value that has round-
// tripped through the apiserver, so status.metadata.releaseDate -- always
// UTC on the wire (MarshalJSON forces UTC) -- comes back out of a live
// client.Get in the RECONCILING REPLICA's local zone. A midnight-UTC
// release date on any host west of UTC then reads one calendar day (and,
// for a January release, one calendar year) earlier than what the provider
// sent, and that wrong year lands directly in the folder name. This is the
// exact bug and fix audiobook.namingContext documents for
// AudiobookMetadata.ReleaseDate (app/catalog/controller/audiobook/path.go);
// buildAlbumMetadataAC has no equivalent plain-int Year field to sidestep
// it the way Movie's own ingestion-time capture does (pkg/metadata/clients/
// tmdb's t.Year() call, on the freshly parsed, not-yet-round-tripped
// time.Time), so this is the one place in this package a date becomes a
// year, and the one place the round-trip matters.
func ReleaseYear(releaseDate *metav1.Time) int {
	if releaseDate == nil {
		return 0
	}
	return releaseDate.Time.UTC().Year()
}

// Path resolves the folder an Album is stored under, given artistPath (the
// owning Artist's OWN already-resolved status.path) and a naming.Context
// describing this album.
//
// naming.Engine.BuildFolder(MediaKindAlbum, ctx) renders
// "<artist folder>/<album folder>" as one joined path (preset.go's own
// path.Join) -- but its artist segment is recomputed from ctx.ArtistName
// via the dialect's plain "{Artist Name}" template, which cannot see
// Artist.Spec.Folder (a manual override the Artist reconciler's own Path
// already resolved into artistPath). Recomputing it here would let the two
// disagree whenever an operator sets spec.folder on the Artist. Rather than
// add a second, artist-segment-only naming primitive to pkg/naming for this
// one caller, this takes only the trailing (album-only) segment via
// path.Base and joins it onto the Artist's own resolved path instead,
// keeping the two always in agreement. ctx.ArtistName is still required
// (BuildFolder's Album case renders it before path.Base discards it), so
// callers must still set it even though its value never appears in the
// result.
func Path(artistPath string, engine naming.Engine, ctx naming.Context) (string, error) {
	full, err := engine.BuildFolder(commonv1.MediaKindAlbum, ctx)
	if err != nil {
		return "", err
	}
	return path.Join(artistPath, path.Base(full)), nil
}
