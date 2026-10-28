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

package projection

import (
	"net/url"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// The Live TV pages' rows (the direct-tuners and ui design 2026-10-27
// §5.4, §5.5): built per page read from the providers and channels the
// ui's cache holds and the tuner's objects in the store, which the caller
// reads and hands in as lookups. Pure, so a page's every case is tested
// without a cluster or a bus.

// LiveTVInputs is what the rows are built from.
type LiveTVInputs struct {
	Providers []clustarrv1.IPTVProvider
	Channels  []clustarrv1.IPTVChannel
	// Playlist answers a provider's playlist entry for a key; false when
	// the playlist lacks it or is not loaded.
	Playlist func(provider types.NamespacedName, key string) (livetvstore.PlaylistEntry, bool)
	// GuideLogo answers the logo the guide index gives an active channel
	// (the one Plex is given), "" when none.
	GuideLogo func(provider types.NamespacedName, number string) string
}

// ChannelRow is one IPTVChannel on the Channels page.
type ChannelRow struct {
	Namespace, Name, ResourceVersion, Provider, Key, TvgID string
	// Number and the name, group and logo the channel goes by: its
	// overrides, else the playlist's. Logo is the source URL; LogoPath the
	// ui's route to the tuner's stored copy of it, "" without a logo.
	Number, DisplayName, Group, Logo, LogoPath string
	// EPG is the spec's guide choice: "" automatic, "dummy", or a guide
	// channel id; Guide is the mapping in use (status.guide).
	EPG, Guide string
	// Active is spec.active; Ready its Ready condition; Missing a channel
	// whose key the provider's playlist no longer has.
	Active, Ready, Missing, NameOverridden bool
	Reason, Message                        string
	// BlockStart and Owner are the Plex block a ready channel is in, and
	// the cluster-plex installation holding it; StreamURL the tuner's
	// <block uri>/stream/<number>.
	BlockStart, Owner, StreamURL string
}

// LiveTVChannels is every channel's row, by number, then provider.
func LiveTVChannels(in LiveTVInputs) []ChannelRow {
	providers := map[types.NamespacedName]*clustarrv1.IPTVProvider{}
	for i := range in.Providers {
		p := &in.Providers[i]
		providers[types.NamespacedName{Namespace: p.Namespace, Name: p.Name}] = p
	}
	rows := make([]ChannelRow, 0, len(in.Channels))
	for i := range in.Channels {
		ch := &in.Channels[i]
		pkey := types.NamespacedName{Namespace: ch.Namespace, Name: ch.Spec.ProviderRef}
		var pe livetvstore.PlaylistEntry
		if in.Playlist != nil {
			pe, _ = in.Playlist(pkey, ch.Spec.Key)
		}
		r := ChannelRow{
			Namespace: ch.Namespace, Name: ch.Name, ResourceVersion: ch.ResourceVersion, Provider: ch.Spec.ProviderRef,
			Key: ch.Spec.Key, TvgID: pe.TvgID, Number: ch.Spec.Number,
			DisplayName:    firstOf(ch.Spec.Name, pe.Name, ch.Spec.Key),
			NameOverridden: ch.Spec.Name != "",
			Group:          firstOf(ch.Spec.Group, pe.Group),
			Active:         ch.Spec.Active, Guide: ch.Status.Guide,
		}
		if e := ch.Spec.EPG; e != nil {
			r.EPG = e.ChannelID
			if e.Dummy {
				r.EPG = "dummy"
			}
		}
		if c := meta.FindStatusCondition(ch.Status.Conditions, clustarrv1.ConditionReady); c != nil {
			r.Ready, r.Reason, r.Message = c.Status == "True", c.Reason, c.Message
			r.Missing = c.Reason == clustarrv1.ReasonMissing
		}
		guideLogo := ""
		if r.Active && in.GuideLogo != nil {
			guideLogo = in.GuideLogo(pkey, r.Number)
		}
		r.Logo = firstOf(guideLogo, ch.Spec.Logo, pe.Logo)
		if r.Logo != "" {
			r.LogoPath = LogoPath(pkey, r.Logo)
		}
		if p := providers[pkey]; p != nil && r.Active && r.Ready {
			if b, ok := BlockOf(p.Status.Blocks, r.Number); ok {
				r.BlockStart, r.Owner = b.Start, b.Owner
				if b.URI != "" {
					r.StreamURL = b.URI + "/stream/" + r.Number
				}
			}
		}
		rows = append(rows, r)
	}
	slices.SortStableFunc(rows, func(a, b ChannelRow) int {
		if c := xmltv.CompareNumbers(a.Number, b.Number); c != 0 {
			return c
		}
		return strings.Compare(a.Namespace+"/"+a.Provider+"/"+a.Name, b.Namespace+"/"+b.Provider+"/"+b.Name)
	})
	return rows
}

// BlockOf is the block number belongs to: the last whose start is at or
// below it (Live TV spec §3.1).
func BlockOf(blocks []clustarrv1.BlockStatus, number string) (clustarrv1.BlockStatus, bool) {
	var out clustarrv1.BlockStatus
	found := false
	for _, b := range blocks {
		if xmltv.CompareNumbers(b.Start, number) <= 0 && (!found || xmltv.CompareNumbers(out.Start, b.Start) < 0) {
			out, found = b, true
		}
	}
	return out, found
}

// LogoPath is the ui's route to provider's stored copy of the logo at
// source (livetvstore.LogoObject), which the browser fetches instead of
// the logo's own host (ADR-0011).
func LogoPath(provider types.NamespacedName, source string) string {
	name := livetvstore.LogoObject(provider, source)
	return "/livetv/logo/" + provider.Namespace + "/" + provider.Name + "/" + name[strings.LastIndex(name, "/")+1:]
}

// SourceRow is one IPTVProvider on the M3U/EPG Manager: its playlist, its
// guide and its blocks.
type SourceRow struct {
	Namespace, Name, Type, Server string
	Enabled, Ready                bool
	// Reason and Message are the Ready condition's, or Valid's when the
	// spec is refused.
	Reason, Message          string
	TunersTotal, TunersInUse int32
	AccountStatus            string
	Expires                  *time.Time
	// ExpiresSoon is an account expiring within a week.
	ExpiresSoon                         bool
	Entries, Groups, Candidates, Active int32
	Updated                             *time.Time
	PlaylistError, AccountError         string
	// GuideSource is spec.guideURL as written, its placeholders
	// unexpanded, else the panel's <server>/xmltv.php with no query:
	// never a credential. GuideFallback is the panel's guide filling the
	// channels a guideURL's lacks; GuidePMS a provider whose guide is
	// Plex's own.
	GuideSource                    string
	GuideFallback, GuidePMS        bool
	GuideChannels, GuideProgrammes int32
	GuideUpdated                   *time.Time
	GuideError                     string
	Blocks                         []BlockRow
}

// BlockRow is one of a provider's blocks: one Plex DVR.
type BlockRow struct {
	Start, First, Last, DeviceID, URI, FriendlyName, Owner, LineupHash, GuideHash string
	Channels, MapBytes                                                            int32
}

// expiresSoonWithin is how close an account's expiry is shown in bold.
const expiresSoonWithin = 7 * 24 * time.Hour

// LiveTVSources is every provider's row, by namespace and name.
func LiveTVSources(providers []clustarrv1.IPTVProvider, now time.Time) []SourceRow {
	rows := make([]SourceRow, 0, len(providers))
	for i := range providers {
		p := &providers[i]
		r := SourceRow{
			Namespace: p.Namespace, Name: p.Name, Type: string(p.Spec.TypeOrDefault()), Server: p.Spec.Server,
			Enabled: p.Spec.EnabledOrDefault(), GuidePMS: p.Spec.EPGSourceOrDefault() == clustarrv1.EPGSourcePMS,
		}
		if c := meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionReady); c != nil {
			r.Ready, r.Reason, r.Message = c.Status == "True", c.Reason, c.Message
		}
		if c := meta.FindStatusCondition(p.Status.Conditions, clustarrv1.ConditionValid); c != nil && c.Status == "False" {
			r.Reason, r.Message = c.Reason, c.Message
		}
		if t := p.Status.Tuners; t != nil {
			r.TunersTotal, r.TunersInUse = t.Total, t.InUse
		}
		if a := p.Status.Account; a != nil {
			r.AccountStatus, r.AccountError = a.Status, a.Error
			if a.ExpiresAt != nil {
				e := a.ExpiresAt.UTC()
				r.Expires, r.ExpiresSoon = &e, e.Sub(now) < expiresSoonWithin
			}
		}
		if pl := p.Status.Playlist; pl != nil {
			r.Entries, r.Groups, r.Candidates, r.PlaylistError = pl.Entries, pl.Groups, pl.Candidates, pl.Error
			r.Updated = timeOf(pl.FetchedAt)
		}
		if l := p.Status.Lineup; l != nil {
			r.Active = l.Active
		}
		m3u := p.Spec.TypeOrDefault() == clustarrv1.ProviderTypeM3U
		if m3u {
			// The playlist URL's host alone: its path and query may name
			// the account.
			if u, err := url.Parse(p.Spec.PlaylistURL); err == nil {
				r.Server = u.Host
			}
		} else {
			r.GuideSource = strings.TrimRight(p.Spec.Server, "/") + "/xmltv.php"
		}
		if g := strings.TrimSpace(p.Spec.GuideURL); g != "" {
			r.GuideSource, r.GuideFallback = g, !m3u
		}
		if g := p.Status.Guide; g != nil {
			r.GuideChannels, r.GuideProgrammes, r.GuideError = g.Channels, g.Programmes, g.Error
			r.GuideUpdated = timeOf(g.FetchedAt)
		}
		for _, b := range p.Status.Blocks {
			r.Blocks = append(r.Blocks, BlockRow{
				Start: b.Start, First: b.First, Last: b.Last, DeviceID: b.DeviceID, URI: b.URI, FriendlyName: b.FriendlyName,
				Owner: b.Owner, LineupHash: short(b.LineupHash), GuideHash: short(b.GuideHash), Channels: b.Channels, MapBytes: b.MapBytes,
			})
		}
		rows = append(rows, r)
	}
	slices.SortFunc(rows, func(a, b SourceRow) int { return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name) })
	return rows
}

func firstOf(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func short(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}

// timeOf is a status time as a *time.Time in UTC, nil when unset.
func timeOf(t *metav1.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
