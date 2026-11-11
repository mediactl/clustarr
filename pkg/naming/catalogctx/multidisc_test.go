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

package catalogctx_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// gatewayReleaseCap is how many of a release group's releases the metadata
// gateway keeps in an Album's status.metadata.releases
// (app/catalog/metadata's buildAlbumMetadataAC), where the Album
// reconciler's SelectRelease reads every release the provider listed.
const gatewayReleaseCap = 50

// TestMultiDiscReadsTheTracksWhenTheSelectedReleaseIsNotListed: a heavily
// reissued album whose most-tracks release, the one SelectRelease takes, is
// a two-CD deluxe edition past the 50 releases the gateway keeps. Its
// summary, and so its media, are not in status.metadata.releases, but the
// tracks BuildTracks flattened from it name medium 2, so the multi-disc
// preset still applies and disc 2's track 1 no longer renders disc 1's
// path (design D19); an album whose tracks lie on one medium keeps the
// flat preset.
func TestMultiDiscReadsTheTracksWhenTheSelectedReleaseIsNotListed(t *testing.T) {
	root := rootAt("/data/music", catalogv1alpha1.RootFolderKindMusic)
	artist := &catalogv1alpha1.Artist{
		Spec:   catalogv1alpha1.ArtistSpec{MusicBrainzID: "382f1005-e9ab-42fd-a9a8-6c8d27fb2b2e"},
		Status: catalogv1alpha1.ArtistStatus{Path: "/data/music/2Pac", Metadata: &catalogv1alpha1.ArtistMetadata{Name: "2Pac"}},
	}
	official := catalogv1alpha1.MusicMetadataProfile{ReleaseStatuses: []string{"official"}}

	for _, tc := range []struct {
		name      string
		deluxe    pkgmetadata.AlbumRelease
		multiDisc bool
	}{
		{"a two-CD release past the cap", release("rel-deluxe", "CD", 14, 13), true},
		{"a one-CD release past the cap", release("rel-deluxe", "CD", 27), false},
	} {
		releases := make([]pkgmetadata.AlbumRelease, 0, 74)
		for i := range 73 {
			releases = append(releases, release(fmt.Sprintf("rel-%02d", i), "CD", 12))
		}
		releases = append(releases, tc.deluxe)

		selected, _ := album.SelectRelease(catalogv1alpha1.AlbumSpec{ReleaseGroupID: "rg-aeom"}, official, "", releases, nil)
		require.NotNil(t, selected, tc.name)
		require.Equal(t, "rel-deluxe", selected.IDs[pkgmetadata.KeyMBRelease], "%s: the most tracks wins", tc.name)
		built, truncated := album.BuildTracks(selected, nil)
		require.False(t, truncated, tc.name)
		var tracks []catalogv1alpha1.Track
		raw, err := json.Marshal(built)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &tracks))

		summaries := make([]catalogv1alpha1.ReleaseSummary, 0, gatewayReleaseCap)
		for _, rel := range releases[:gatewayReleaseCap] {
			summaries = append(summaries, catalogv1alpha1.ReleaseSummary{
				ID: rel.IDs[pkgmetadata.KeyMBRelease], Media: []catalogv1alpha1.Medium{{Number: 1, Format: "CD"}},
			})
		}
		al := &catalogv1alpha1.Album{
			Spec: catalogv1alpha1.AlbumSpec{ReleaseGroupID: "rg-aeom"},
			Status: catalogv1alpha1.AlbumStatus{
				Path: "/data/music/2Pac/All Eyez on Me (1996)",
				Metadata: &catalogv1alpha1.AlbumMetadata{
					Title: "All Eyez on Me", ReleaseDate: utcDate(1996, time.February, 13),
					Releases: summaries, SelectedReleaseID: "rel-deluxe",
				},
				Tracks: tracks,
			},
		}
		require.Nil(t, catalogctx.SelectedRelease(al), "%s: the selected release is past the gateway's 50", tc.name)
		assert.Equal(t, tc.multiDisc, catalogctx.MultiDisc(al), tc.name)

		paths := map[string]bool{}
		for i := range al.Status.Tracks {
			tr := &al.Status.Tracks[i]
			if tr.Number != 1 {
				continue
			}
			got, err := catalogctx.TrackFilePath(root, artist, al, mustAlbum(t, artist, al, tr), ".flac")
			require.NoError(t, err, tc.name)
			assert.False(t, paths[got], "%s: %s rendered twice", tc.name, got)
			paths[got] = true
			if tc.multiDisc {
				assert.NotEqual(t, al.Status.Path, filepath.Dir(got), "%s: medium %d has a folder of its own", tc.name, tr.Medium)
			} else {
				assert.Equal(t, al.Status.Path, filepath.Dir(got), "%s: one medium keeps the flat preset", tc.name)
			}
		}
		want := 1
		if tc.multiDisc {
			want = 2
		}
		assert.Len(t, paths, want, "%s: every medium's track 1", tc.name)
	}

	// A release status.metadata.releases lists with its media still decides,
	// whatever its tracks say.
	listed := &catalogv1alpha1.Album{Status: catalogv1alpha1.AlbumStatus{
		Metadata: &catalogv1alpha1.AlbumMetadata{
			Title: "All Eyez on Me", SelectedReleaseID: "rel-1cd",
			Releases: []catalogv1alpha1.ReleaseSummary{{ID: "rel-1cd", Media: []catalogv1alpha1.Medium{{Number: 1, Format: "CD"}}}},
		},
		Tracks: []catalogv1alpha1.Track{{RecordingID: "r", Medium: 2, Number: 1}},
	}}
	assert.False(t, catalogctx.MultiDisc(listed), "the listed release's one medium")
}

// release is a MusicBrainz release of media, one per count, each that many
// tracks, as the gateway's lookup hands the Album reconciler.
func release(id, format string, counts ...int) pkgmetadata.AlbumRelease {
	rel := pkgmetadata.AlbumRelease{
		IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: id}, Title: "All Eyez on Me", Status: "Official",
	}
	for m, n := range counts {
		medium := pkgmetadata.Medium{Position: int32(m + 1), Format: format, TrackCount: int32(n)}
		for i := range n {
			medium.Tracks = append(medium.Tracks, pkgmetadata.Track{
				IDs:      pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: fmt.Sprintf("%s-rec-%d-%d", id, m+1, i+1)},
				Title:    fmt.Sprintf("Track %d", i+1),
				Position: int32(i + 1), MediumNumber: int32(m + 1),
			})
		}
		rel.Media = append(rel.Media, medium)
		rel.TrackCount += int32(n)
	}
	return rel
}
