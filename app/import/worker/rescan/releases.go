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
			logging.FromContext(ctx).DebugContext(ctx, "rescan: an album's releases are not to be had; its files are attributed on its selected release alone",
				"releaseGroup", group, "err", err)
			rs = nil
		}
		if g.st.releases == nil {
			g.st.releases = map[string][]metadata.AlbumRelease{}
		}
		g.st.releases[group] = rs
		return rs
	}
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
