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
	"path/filepath"
	"slices"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ArtistAlbumsLookup answers every release group credited to the artist of
// MusicBrainz id artistMBID, of every type (Worker.ArtistAlbums).
type ArtistAlbumsLookup func(ctx context.Context, artistMBID string) ([]metadata.Album, error)

// tagAlbum is the album a music file's tags name when no Album of its
// artist is that album: a release group credited to the artist that the
// artist's metadata profile does not list -- a compilation, a mixtape, a
// single, a remix album -- matched by MatchAlbum's own rules (the
// release-group tag, the album folder, the ALBUM tag's title through
// albumTitleTiers) against the artist's every release group, those an Album
// already holds aside (owner, 2026-11-16: "Ensure that Unmatched has no
// remaining music entries"; the library is a person's picks, often a few
// tracks of an album, so the album a file names is wanted whatever the
// profile, and only that one). The proposal names it by names.Album, the
// Artist fan-out's name, so the two never make two objects of one group.
// ok is false when no release group, or more than one, is the file's, or
// the artist's release groups are not to be had this walk.
func (w *Worker) tagAlbum(
	ctx context.Context, g *gateway, path, rel string, tags *commonv1.MediaTags, artist string,
) (*schema.ItemCreate, bool) {
	if tags == nil {
		return nil, false
	}
	i := slices.IndexFunc(g.st.nonVideo.artists, func(c ArtistCandidate) bool { return c.Name == artist })
	if i < 0 || g.st.nonVideo.artists[i].MusicBrainzID == "" {
		return nil, false
	}
	groups := w.artistAlbums(ctx, g, g.st.nonVideo.artists[i].MusicBrainzID)
	if len(groups) == 0 {
		return nil, false
	}
	held := map[string]bool{}
	for _, c := range g.st.nonVideo.albums {
		if c.Artist == artist {
			held[c.ReleaseGroupID] = true
		}
	}
	segment, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
	var cands []AlbumCandidate
	titles := map[string]string{}
	// A file numbered past 3, or tagged with more than 3 tracks, is not a
	// single's: Chief Keef's mixtape DIRTY NACHOS and his single Dirty Nachos
	// otherwise name every file of the mixtape twice.
	notSingle := tags.TrackNumber > 3 || tags.TrackTotal > 3
	for _, alb := range groups {
		id := alb.IDs[metadata.KeyMBReleaseGroup]
		if id == "" || held[id] || (notSingle && strings.EqualFold(alb.PrimaryType, "Single")) {
			continue
		}
		year := 0
		if alb.ReleaseDate != nil {
			year = alb.ReleaseDate.UTC().Year()
		}
		cands = append(cands, AlbumCandidate{
			Name: id, Title: alb.Title, Year: year, ReleaseGroupID: id, ArtistNames: []string{segment},
		})
		titles[id] = alb.Title
	}
	m := MatchAlbum(path, rel, tags, cands)
	if m.Unmatched {
		return nil, false
	}
	group := m.Ref.Name
	return &schema.ItemCreate{
		Kind: string(commonv1.MediaKindAlbum), Name: names.Album(artist, group),
		Title: titles[group], ReleaseGroupID: group, ArtistRef: artist,
	}, true
}

// artistAlbums is the artist's every release group, asked of the gateway
// once a walk per artist (g, so the walk heartbeats under IdentifyTimeout);
// a failure is logged and memoised as none.
func (w *Worker) artistAlbums(ctx context.Context, g *gateway, mbid string) []metadata.Album {
	if groups, ok := g.st.artistAlbums[mbid]; ok {
		return groups
	}
	var groups []metadata.Album
	err := g.call(ctx, func(ctx context.Context) error {
		var err error
		if w.ArtistAlbums != nil {
			groups, err = w.ArtistAlbums(ctx, mbid)
		} else {
			groups, err = w.artistAlbumsRPC(ctx, mbid)
		}
		return err
	})
	if err != nil {
		logging.FromContext(ctx).InfoContext(ctx, "rescan: an artist's release groups are not to be had; "+
			"a file naming an album its profile leaves out stays listed this scan", "artist", mbid, "err", err)
		groups = nil
	}
	if g.st.artistAlbums == nil {
		g.st.artistAlbums = map[string][]metadata.Album{}
	}
	g.st.artistAlbums[mbid] = groups
	return groups
}

// artistAlbumsRPC asks the gateway's lookup verb for an artist's album list
// (the Artist fan-out's request, which the gateway answers from its
// album-list cache for a day).
func (w *Worker) artistAlbumsRPC(ctx context.Context, mbid string) ([]metadata.Album, error) {
	if w.Bus == nil {
		return nil, fmt.Errorf("rescan: no bus to ask for artist %s's release groups with", mbid)
	}
	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindAlbum, IDs: map[string]string{metadata.KeyMBArtist: mbid}}
	if err := w.Bus.Request(ctx, events.RPCMetadataLookup, req, &resp); err != nil {
		return nil, fmt.Errorf("rescan: look up artist %s's albums: %w", mbid, events.DescribeRequestError(events.RPCMetadataLookup, err))
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("rescan: look up artist %s's albums: %s", mbid, resp.Error)
	}
	out := make([]metadata.Album, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var alb metadata.Album
		if err := json.Unmarshal(raw, &alb); err != nil {
			return nil, fmt.Errorf("rescan: decode artist %s's album: %w", mbid, err)
		}
		out = append(out, alb)
	}
	return out, nil
}

// proposeAlbum reports the album c a file's tags name, once a walk, for the
// manager's scan applier to create; the file itself waits for its tracks,
// as an artist folder's files wait for its albums.
func (w *Worker) proposeAlbum(ctx context.Context, st *scanState, path string, info os.FileInfo, c *schema.ItemCreate) error {
	if st.proposedAlbums[c.ReleaseGroupID] {
		return nil
	}
	if st.proposedAlbums == nil {
		st.proposedAlbums = map[string]bool{}
	}
	st.proposedAlbums[c.ReleaseGroupID] = true
	if st.task.DryRun {
		st.progress.ItemsCreated++
		return nil
	}
	obs := schema.ScanObservation{
		Kind: schema.ScanObservationPresent, Path: path,
		SizeBytes: info.Size(), ModTime: info.ModTime().UTC(),
		Attribution: &schema.ScanAttribution{
			Kind: c.Kind, Name: c.Name, ProviderID: c.ReleaseGroupID, Evidence: "tags", Create: c,
		},
	}
	if err := w.observe(ctx, st, obs); err != nil {
		return err
	}
	logging.FromContext(ctx).Info("proposed an album a file's tags name, which the artist's profile does not list",
		"artist", c.ArtistRef, "album", c.Name, "releaseGroup", c.ReleaseGroupID, "title", c.Title)
	return nil
}
