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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
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

// Path resolves the folder an Album is stored under: catalogctx.AlbumFolder,
// the rule the import's TrackFilePath places files by (2026-11-10 non-video
// design §5.5; wave F) -- the album segment of the preset, sanitized and
// held under the root, below the Artist's own folder (its pinned spec.folder
// as written, else its status.path, else the artist preset), so the
// status.path this controller writes and the folder the import creates
// cannot diverge. An album whose title has not arrived is the artist's
// folder; a title the rule cannot render is an error the reconciler reports
// as catalogctx.ReasonFolderUnrenderable.
func Path(root *catalogv1alpha1.RootFolder, artist *catalogv1alpha1.Artist, c naming.Context) (string, error) {
	return catalogctx.AlbumFolder(root, artist, c)
}
