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
	"k8s.io/utils/ptr"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// maxTracks mirrors AlbumStatus.Tracks' own +kubebuilder:validation:MaxItems
// cap (album_types.go), repeated here as a named constant so BuildTracks can
// compare against it without depending on the CRD marker.
const maxTracks = 200

// SelectRelease picks the one release of releases this Album's tracks are
// taken from, honouring spec's pin/AnyReleaseOk exactly as AlbumSpec's own
// field doc comments describe:
//
//   - spec.ReleaseID, when set and present among releases, wins outright.
//   - spec.ReleaseID set but NOT present: spec.AnyReleaseOk (default true)
//     decides whether to fall back to another release, or report no
//     selection at all (false -- the user pinned a specific release and
//     declined every other one).
//   - No ReleaseID pinned: the first release in releases wins. releases'
//     own order is whatever the provider returned; pkg/metadata.Album
//     carries no separate popularity/preference ranking to sort by, and
//     picking anything other than "first" would be inventing a preference
//     this task has no data to justify.
//
// Returns (nil, false) when there is nothing to select from at all (no
// releases) -- always true against a real request today, since
// pkg/metadata/clients/musicbrainz's mapAlbum does not currently populate
// Album.Releases from either ArtistProvider call; see this package's
// doc.go.
func SelectRelease(spec catalogv1alpha1.AlbumSpec, releases []pkgmetadata.AlbumRelease) (*pkgmetadata.AlbumRelease, bool) {
	if len(releases) == 0 {
		return nil, false
	}
	if spec.ReleaseID != nil && *spec.ReleaseID != "" {
		for i := range releases {
			if releases[i].IDs[pkgmetadata.KeyMBRelease] == *spec.ReleaseID {
				return &releases[i], true
			}
		}
		if !ptr.Deref(spec.AnyReleaseOk, true) {
			return nil, false
		}
	}
	return &releases[0], true
}

// BuildTracks flattens rel's media into the Track apply configurations
// AlbumStatus.Tracks holds. AbsoluteNumber counts cumulatively across every
// medium in rel.Media, in medium order (Medium.Position ascending is
// assumed to already be the provider's own order; this function does not
// re-sort, the same "don't invent an ordering the provider didn't give"
// rule this task applies elsewhere).
//
// A track with no MusicBrainz recording id is dropped: Track.RecordingID is
// +required and this list's own +listMapKey, so a blank id would collide
// with every other dropped entry -- the same rule buildAlbumMetadataAC's
// Releases loop and buildBookMetadataAC's Editions loop already apply
// (catalogarr/metadata/patch.go).
//
// truncated reports whether rel carries more than maxTracks recordings with
// a usable id -- AlbumStatus.Tracks' own doc comment ("Releases with more
// than 200 tracks raise the Invalid condition instead of being truncated
// silently"), so the caller must surface this as AlbumConditionInvalid
// rather than silently accepting a short list.
func BuildTracks(rel *pkgmetadata.AlbumRelease) (tracks []*catalogac.TrackApplyConfiguration, truncated bool) {
	if rel == nil {
		return nil, false
	}
	var absolute int32
	for _, medium := range rel.Media {
		for _, tr := range medium.Tracks {
			absolute++
			recordingID := tr.IDs[pkgmetadata.KeyMBRecording]
			if recordingID == "" {
				continue
			}
			if len(tracks) >= maxTracks {
				truncated = true
				continue
			}
			tracks = append(tracks, catalogac.Track().
				WithRecordingID(recordingID).
				WithMedium(medium.Position).
				WithNumber(tr.Position).
				WithAbsoluteNumber(absolute).
				WithTitle(tr.Title).
				WithDurationMs(int32(tr.Duration.Milliseconds())))
		}
	}
	return tracks, truncated
}
