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
	"github.com/mediactl/clustarr/app/catalog/controller/artist"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// maxTracks mirrors AlbumStatus.Tracks' own +kubebuilder:validation:MaxItems
// cap (album_types.go), repeated here as a named constant so BuildTracks can
// compare against it without depending on the CRD marker.
const maxTracks = 200

// Selection says how SelectRelease arrived at its answer. It is the reason
// the reconciler reports on its TracksSynced condition.
type Selection string

// Selection outcomes. The first three chose a release; the last three did
// not.
const (
	// SelectionPinned: spec.releaseID named a release the group has.
	SelectionPinned Selection = "Pinned"
	// SelectionKept: the release selected before is still a candidate and
	// nothing displaces it.
	SelectionKept Selection = "Kept"
	// SelectionBest: the candidate with the most files, then the most
	// tracks.
	SelectionBest Selection = "Best"
	// SelectionNoReleases: the release group lists no releases (yet).
	SelectionNoReleases Selection = "NoReleases"
	// SelectionPinnedReleaseMissing: spec.releaseID names a release the
	// group does not have, and spec.anyReleaseOk is false.
	SelectionPinnedReleaseMissing Selection = "PinnedReleaseMissing"
	// SelectionNoAcceptedRelease: no release has tracks and a status the
	// Artist's metadata profile accepts.
	SelectionNoAcceptedRelease Selection = "NoAcceptedRelease"
)

// SelectRelease picks the one release of releases this Album's tracks are
// taken from -- the value status.metadata.selectedReleaseID records. It is
// Lidarr's rule (src/NzbDrone.Core, Lidarr/Lidarr da7b4dfb), adapted to a
// spec that pins instead of monitoring:
//
//  1. spec.releaseID, when the group has that release, wins outright. When it
//     does not, spec.anyReleaseOk (default true) decides between falling
//     through to the rules below and selecting nothing (false: the user
//     pinned one release and declined every other).
//  2. The candidates are the releases with at least one track and a status
//     profile.ReleaseStatuses accepts (artist.ReleaseStatusAccepted).
//     Lidarr drops track-less releases the same way (SkyHookProxy.MapAlbum:
//     `.Where(x => x.TrackCount > 0)`). Lidarr applies release statuses to
//     the whole album instead (SkyHookProxy.FilterAlbums keeps an album
//     with any accepted release, then may monitor any of its releases);
//     narrowing the candidates means an "official only" profile never takes
//     a bootleg's track list, and an album with no accepted release at all
//     -- one Lidarr would have filtered out -- selects nothing and says so.
//  3. previous, the release selected before, is kept while it is still a
//     candidate: Lidarr's RefreshAlbumService.MonitorSingleRelease keeps the
//     monitored release on every refresh. With spec.anyReleaseOk it gives
//     way to a candidate holding strictly more of the album's files, which
//     is where Lidarr's importer moves the monitored release when files
//     match another one.
//  4. Otherwise the candidate with the most files wins, then the one with
//     the most tracks (MonitorSingleRelease's OrderByDescending(files)
//     .ThenByDescending(TrackCount)), then the provider's order.
//
// files maps a recording MBID to the MediaFile holding it (FilesByRecording);
// a release's file count is how many of its recordings are in it.
func SelectRelease(
	spec catalogv1alpha1.AlbumSpec,
	profile catalogv1alpha1.MusicMetadataProfile,
	previous string,
	releases []pkgmetadata.AlbumRelease,
	files map[string]string,
) (*pkgmetadata.AlbumRelease, Selection) {
	if len(releases) == 0 {
		return nil, SelectionNoReleases
	}
	if pin := ptr.Deref(spec.ReleaseID, ""); pin != "" {
		for i := range releases {
			if releases[i].IDs[pkgmetadata.KeyMBRelease] == pin {
				return &releases[i], SelectionPinned
			}
		}
		if !ptr.Deref(spec.AnyReleaseOk, true) {
			return nil, SelectionPinnedReleaseMissing
		}
	}

	var candidates []*pkgmetadata.AlbumRelease
	for i := range releases {
		rel := &releases[i]
		if releaseID(rel) == "" || trackCount(rel) == 0 || !artist.ReleaseStatusAccepted(profile, rel.Status) {
			continue
		}
		candidates = append(candidates, rel)
	}
	if len(candidates) == 0 {
		return nil, SelectionNoAcceptedRelease
	}

	var best *pkgmetadata.AlbumRelease
	var kept *pkgmetadata.AlbumRelease
	for _, rel := range candidates {
		if previous != "" && releaseID(rel) == previous {
			kept = rel
		}
		if best == nil || better(rel, best, files) {
			best = rel
		}
	}
	if kept != nil && (!ptr.Deref(spec.AnyReleaseOk, true) || filesOn(best, files) <= filesOn(kept, files)) {
		return kept, SelectionKept
	}
	return best, SelectionBest
}

// better orders SelectRelease's candidates: more files, then more tracks. A
// tie keeps the incumbent, so the provider's order breaks it.
func better(a, b *pkgmetadata.AlbumRelease, files map[string]string) bool {
	if fa, fb := filesOn(a, files), filesOn(b, files); fa != fb {
		return fa > fb
	}
	return trackCount(a) > trackCount(b)
}

func releaseID(rel *pkgmetadata.AlbumRelease) string {
	return rel.IDs[pkgmetadata.KeyMBRelease]
}

// trackCount is the release's own track count, or, when the provider left
// it zero, the tracks its media list.
func trackCount(rel *pkgmetadata.AlbumRelease) int32 {
	if rel.TrackCount > 0 {
		return rel.TrackCount
	}
	var n int32
	for _, m := range rel.Media {
		n += int32(len(m.Tracks))
	}
	return n
}

// filesOn counts rel's distinct recordings that have a file.
func filesOn(rel *pkgmetadata.AlbumRelease, files map[string]string) int {
	seen := map[string]bool{}
	n := 0
	for _, m := range rel.Media {
		for _, tr := range m.Tracks {
			id := tr.IDs[pkgmetadata.KeyMBRecording]
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			if files[id] != "" {
				n++
			}
		}
	}
	return n
}

// BuildTracks flattens rel's media into the Track apply configurations
// AlbumStatus.Tracks holds, with each track's fileRef taken from files (the
// MediaFile holding that recording, FilesByRecording). AbsoluteNumber counts
// cumulatively across every medium in rel.Media, in medium order
// (Medium.Position ascending is assumed to already be the provider's own
// order; this function does not re-sort).
//
// A track with no MusicBrainz recording id is dropped: Track.RecordingID is
// +required and this list's own +listMapKey, so a blank id would collide
// with every other dropped entry -- the same rule buildAlbumMetadataAC's
// Releases loop and buildBookMetadataAC's Editions loop already apply
// (catalogarr/metadata/patch.go). For the same reason a recording the
// release lists twice -- the same audio on a CD and on the DVD beside it --
// is kept once, at its first position: two entries with one key would make
// the apiserver refuse the whole status apply.
//
// truncated reports whether rel carries more than maxTracks distinct
// recordings with a usable id -- AlbumStatus.Tracks' own doc comment
// ("Releases with more than 200 tracks raise the Invalid condition instead
// of being truncated silently"), so the caller must surface this as
// AlbumConditionInvalid rather than silently accepting a short list.
func BuildTracks(rel *pkgmetadata.AlbumRelease, files map[string]string) (tracks []*catalogac.TrackApplyConfiguration, truncated bool) {
	if rel == nil {
		return nil, false
	}
	seen := map[string]bool{}
	var absolute int32
	for _, medium := range rel.Media {
		for _, tr := range medium.Tracks {
			absolute++
			recordingID := tr.IDs[pkgmetadata.KeyMBRecording]
			if recordingID == "" || seen[recordingID] {
				continue
			}
			seen[recordingID] = true
			if len(tracks) >= maxTracks {
				truncated = true
				continue
			}
			tc := catalogac.Track().
				WithRecordingID(recordingID).
				WithMedium(medium.Position).
				WithNumber(tr.Position).
				WithAbsoluteNumber(absolute).
				WithTitle(tr.Title).
				WithDurationMs(int32(tr.Duration.Milliseconds()))
			if f := files[recordingID]; f != "" {
				tc = tc.WithFileRef(f)
			}
			tracks = append(tracks, tc)
		}
	}
	return tracks, truncated
}

// TracksFromStatus re-renders the track list an Album already carries, with
// each fileRef refreshed from files. It is what the reconciler declares when
// it could not fetch the release group this pass: a transient RPC failure
// must not release a listing it had, and a file imported meanwhile still
// reaches its track.
func TracksFromStatus(existing []catalogv1alpha1.Track, files map[string]string) []*catalogac.TrackApplyConfiguration {
	if len(existing) == 0 {
		return nil
	}
	tracks := make([]*catalogac.TrackApplyConfiguration, 0, len(existing))
	for _, tr := range existing {
		tc := catalogac.Track().
			WithRecordingID(tr.RecordingID).
			WithMedium(tr.Medium).
			WithNumber(tr.Number).
			WithAbsoluteNumber(tr.AbsoluteNumber).
			WithTitle(tr.Title).
			WithDurationMs(tr.DurationMs).
			WithExplicit(tr.Explicit)
		if f := files[tr.RecordingID]; f != "" {
			tc = tc.WithFileRef(f)
		}
		tracks = append(tracks, tc)
	}
	return tracks
}
