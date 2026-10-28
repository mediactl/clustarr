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

package ui

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
)

// The Live TV section's shared reads (the direct-tuners and ui design
// 2026-10-27 §5): the providers and channels through Options.Reader, as the
// Settings page reads its kinds, and the tuner's objects through the
// Live TV store.

// listIPTVProviders lists every IPTVProvider, by namespace and name.
func (s *Server) listIPTVProviders(ctx context.Context) []clustarrv1.IPTVProvider {
	if s.opts.Reader == nil {
		return nil
	}
	var list clustarrv1.IPTVProviderList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list IPTV providers", "error", err)
		return nil
	}
	slices.SortFunc(list.Items, func(a, b clustarrv1.IPTVProvider) int {
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	return list.Items
}

// listIPTVChannels lists every IPTVChannel.
func (s *Server) listIPTVChannels(ctx context.Context) []clustarrv1.IPTVChannel {
	if s.opts.Reader == nil {
		return nil
	}
	var list clustarrv1.IPTVChannelList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list IPTV channels", "error", err)
		return nil
	}
	return list.Items
}

// liveTVInputs is the rows' inputs: every provider and channel, and the
// store's playlists and guide indexes as lookups.
func (s *Server) liveTVInputs(ctx context.Context) projection.LiveTVInputs {
	in := projection.LiveTVInputs{Providers: s.listIPTVProviders(ctx), Channels: s.listIPTVChannels(ctx)}
	playlists := map[types.NamespacedName]*playlistView{}
	guides := map[types.NamespacedName]*guideView{}
	for _, p := range in.Providers {
		key := types.NamespacedName{Namespace: p.Namespace, Name: p.Name}
		if v, ok := s.liveTV.Playlist(ctx, key); ok {
			playlists[key] = v
		}
		if v, ok := s.liveTV.GuideIndex(ctx, key); ok {
			guides[key] = v
		}
	}
	in.Playlist = func(provider types.NamespacedName, key string) (livetvstore.PlaylistEntry, bool) {
		v := playlists[provider]
		if v == nil {
			return livetvstore.PlaylistEntry{}, false
		}
		i, ok := v.ByKey[key]
		if !ok {
			return livetvstore.PlaylistEntry{}, false
		}
		return v.Entries[i], true
	}
	in.GuideLogo = func(provider types.NamespacedName, number string) string {
		if g := guides[provider]; g != nil {
			return g.Logos[number]
		}
		return ""
	}
	return in
}

// logoHash is a stored logo's name segment (livetvstore.LogoObject).
var logoHash = regexp.MustCompile(`^[0-9a-f]{32}$`)

// maxLiveTVLogoBytes bounds a stored logo the ui serves, as the tuner
// bounds what it stores.
const maxLiveTVLogoBytes = 4 << 20

// handleLiveTVLogo serves a channel logo the tuner stored, processed, so
// the browser never asks the logo's own host for it (ADR-0011): 404 for a
// logo not stored, ETag its digest.
func (s *Server) handleLiveTVLogo(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if s.opts.LiveTV == nil || !logoHash.MatchString(hash) {
		http.NotFound(w, r)
		return
	}
	name := "logo/" + r.PathValue("namespace") + "/" + r.PathValue("name") + "/" + hash
	ctx, cancel := context.WithTimeout(r.Context(), liveTVReadTimeout)
	defer cancel()
	info, err := s.opts.LiveTV.Info(ctx, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	etag := `"` + info.Digest + `"`
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, rc, err := s.opts.LiveTV.Get(ctx, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxLiveTVLogoBytes+1))
	if err != nil || len(b) > maxLiveTVLogoBytes {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}
