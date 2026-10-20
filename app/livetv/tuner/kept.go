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

package tuner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/iptv/m3u"
)

// KeptPlaylist is what a restart needs of the last good playlist to serve
// the lineup before its first fetch: the playlist entries of the provider's
// channels, stream URLs included, and the counts its status reports. Only
// the tuner held the playlist, so a manager restart while the provider
// could not be fetched left every tune 404. On 2026-10-20 tvload.win
// answered too slowly for the fetch's header timeout, and the 19:39 UTC
// rollout's restart took Live TV down. The stream URLs carry the account's
// credentials, so the controller keeps this in a Secret, never in the
// object store the ui reads (spec §5.4).
type KeptPlaylist struct {
	Hash       string    `json:"hash"`
	FetchedAt  time.Time `json:"fetchedAt"`
	Entries    int       `json:"entries"`
	Groups     int       `json:"groups"`
	Candidates int       `json:"candidates"`
	// Channels are the candidate entries of every configured channel,
	// active or not, by key, so a restored lineup places each channel as
	// the fetched one did.
	Channels []m3u.Entry `json:"channels"`
}

// Digest identifies k's content: the controller rewrites its Secret only
// when the digest moves.
func (k KeptPlaylist) Digest() string {
	b, _ := json.Marshal(k)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// keptPlaylist is a render's KeptPlaylist, from its inputs: pl with its
// counts, and the configured channels' candidate entries, in key order.
func keptPlaylist(pl PlaylistState, channels []ChannelConfig, candidate map[string]m3u.Entry) *KeptPlaylist {
	k := &KeptPlaylist{
		Hash: pl.Hash, FetchedAt: pl.FetchedAt,
		Entries: pl.Entries, Groups: pl.Groups, Candidates: pl.Candidates,
	}
	seen := map[string]bool{}
	for _, ch := range channels {
		if e, ok := candidate[ch.Key]; ok && !seen[ch.Key] {
			seen[ch.Key] = true
			k.Channels = append(k.Channels, e)
		}
	}
	slices.SortFunc(k.Channels, func(a, b m3u.Entry) int { return strings.Compare(a.Key, b.Key) })
	return k
}

// Kept is the provider's playlist as the controller keeps it, from this
// process' last fetch; false before one, and while the tuner serves only a
// restored playlist.
func (t *Tuner) Kept(key types.NamespacedName) (KeptPlaylist, bool) {
	t.mu.Lock()
	p := t.providers[key]
	t.mu.Unlock()
	if p == nil {
		return KeptPlaylist{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.kept == nil {
		return KeptPlaylist{}, false
	}
	k := *p.kept
	k.Channels = slices.Clone(k.Channels)
	return k, true
}

// Restore serves k until the provider's first fetch in this process, so a
// restart while the provider cannot be fetched still tunes its channels.
// The status reads "serving the last good playlist" with the fetch's error.
// Restore does nothing once a fetch has succeeded, for a provider the tuner
// does not serve, or for a k with no channel.
func (t *Tuner) Restore(key types.NamespacedName, k KeptPlaylist) {
	t.mu.Lock()
	p := t.providers[key]
	t.mu.Unlock()
	if p == nil || len(k.Channels) == 0 {
		return
	}
	p.mu.Lock()
	if p.removed || p.playlistFetched {
		p.mu.Unlock()
		return
	}
	r := k
	r.Channels = slices.Clone(k.Channels)
	p.restored = &r
	p.entries = r.Channels
	p.playlistFetched = true
	p.playlist.Hash, p.playlist.FetchedAt = r.Hash, r.FetchedAt
	// The ui's playlist object is the whole playlist from the fetch this was
	// kept from; the restored channels alone must not replace it.
	p.storedPlaylistHash = r.Hash
	t.markWantedGuidesDue(p)
	p.mu.Unlock()
	t.scheduleRender(p)
	t.poke()
}
