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

package rescan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ReleasesLookup answers every release of the release group group, tracks
// and all (Worker.Releases).
type ReleasesLookup func(ctx context.Context, group string) ([]metadata.AlbumRelease, error)

// albumReleases is fileimport.AttributeTrackIn's ReleasesFunc for the
// album of release group group: the album's every release, which the walk
// asks the gateway for (g, so it heartbeats under IdentifyTimeout) only
// when a file's selected release does not settle it, and once a walk per
// group, a failure memoised as none. A walk of a flat artist folder reaches
// one album's files many times over; the gateway answers from its metadata
// cache. nil for an album without a group.
func (w *Worker) albumReleases(ctx context.Context, g *gateway, group string) fileimport.ReleasesFunc {
	if group == "" {
		return nil
	}
	return func() []metadata.AlbumRelease {
		if rs, ok := g.st.releases[group]; ok {
			return rs
		}
		var rs []metadata.AlbumRelease
		err := g.call(ctx, func(ctx context.Context) error {
			var err error
			if w.Releases != nil {
				rs, err = w.Releases(ctx, group)
			} else {
				rs, err = w.releasesRPC(ctx, group)
			}
			return err
		})
		if err != nil {
			logging.FromContext(ctx).InfoContext(ctx, "rescan: an album's releases are not to be had; its files are attributed on its selected release alone",
				"releaseGroup", group, "err", err)
			rs = nil
			if g.st.releaseErrs == nil {
				g.st.releaseErrs = map[string]error{}
			}
			g.st.releaseErrs[group] = err
		}
		if g.st.releases == nil {
			g.st.releases = map[string][]metadata.AlbumRelease{}
		}
		g.st.releases[group] = rs
		return rs
	}
}

// heldWithoutReleases is reason, for a file AttributeTrackIn held, with why
// its album's other releases were not consulted when this walk could not
// read them: the hold is the gateway's failure, which the next scan asks
// again, not a verdict on the file.
func heldWithoutReleases(st *scanState, group, reason string) string {
	if err := st.releaseErrs[group]; err != nil && reason != fileimport.ErrTracksPending {
		return fmt.Sprintf("%s; the album's other releases could not be read this scan (%v), so the next scan tries them again", reason, err)
	}
	return reason
}

// releasesRPC asks the gateway's lookup verb for release group group's
// document (the gateway answers it from its metadata cache, where the
// album's own refresh left it) and answers its releases.
func (w *Worker) releasesRPC(ctx context.Context, group string) ([]metadata.AlbumRelease, error) {
	if w.Bus == nil {
		return nil, fmt.Errorf("rescan: no bus to ask for release group %s's releases with", group)
	}
	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindAlbum, IDs: map[string]string{metadata.KeyMBReleaseGroup: group}}
	if err := w.Bus.Request(ctx, events.RPCMetadataLookup, req, &resp); err != nil {
		return nil, fmt.Errorf("rescan: look up release group %s: %w", group, events.DescribeRequestError(events.RPCMetadataLookup, err))
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("rescan: look up release group %s: %s", group, resp.Error)
	}
	var doc metadata.Album
	if err := json.Unmarshal(resp.Result, &doc); err != nil {
		return nil, fmt.Errorf("rescan: decode release group %s: %w", group, err)
	}
	return doc.Releases, nil
}

// reattributeTrack is a full scan's second look at a recorded album file's
// track: the file is attributed again within the Album it is recorded on
// (fileimport.AttributeTrackIn, the album's other releases asked for as for
// a new file) -- its Album never changes (P41) -- and a track it now fits
// better is reported with the record as its basis, which the applier
// corrects (2026-11-16: 3,664 of talos's album files had been attributed by
// number against the wrong edition, and no scan could correct one). The
// walk's track holds follow the correction, so a shifted album is put right
// in one pass whatever its files' order. A file the releases cannot place
// keeps its record, which the rename pass does not move while its title tag
// names another song (TrackTitleMismatch).
func (w *Worker) reattributeTrack(ctx context.Context, st *scanState, path, rel string, info os.FileInfo, existing *catalogv1alpha1.MediaFile) error {
	ref := existing.Spec.MediaRef
	cand := st.nonVideo.album(ref.Name)
	if cand == nil || len(cand.Tracks) == 0 {
		return w.proposeFile(ctx, st, path, info, existing, ref, nil, nil, "recorded")
	}
	if err := st.beatNow(ctx, false); err != nil {
		return err
	}
	_, ap := w.freshNonVideoSpec(ctx, commonv1.MediaKindAlbum, path, rel)
	g := &gateway{w: w, st: st}
	id, _ := fileimport.AttributeTrackIn(cand.Tracks, cand.Media, w.albumReleases(ctx, g, cand.ReleaseGroupID), ap, rel, len(cand.Tracks) == 1)
	if g.err != nil {
		return g.err
	}
	evidence := "recorded"
	if id != "" && id != ref.Track {
		idx := st.nonVideo
		if held, ok := idx.tracks[trackKey(ref.Name, ref.Track)]; ok && held == rel {
			delete(idx.tracks, trackKey(ref.Name, ref.Track))
		}
		idx.tracks[trackKey(ref.Name, id)] = rel
		logging.FromContext(ctx).InfoContext(ctx, "rescan: a recorded album file is another track of its album",
			"album", ref.Name, "path", rel, "from", ref.Track, "to", id)
		ref.Track, evidence = id, "tags"
	}
	return w.proposeFile(ctx, st, path, info, existing, ref, nil, nil, evidence)
}

// album is the root's album candidate named name, nil when none is.
func (idx *nonVideoIndex) album(name string) *AlbumCandidate {
	for i := range idx.albums {
		if idx.albums[i].Name == name {
			return &idx.albums[i]
		}
	}
	return nil
}
