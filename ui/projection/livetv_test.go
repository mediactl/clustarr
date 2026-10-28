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

package projection_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/ui/projection"
)

const tvAddress = "clustarr-livetv.media.svc.cluster.local:80"

func tvProvider() clustarrv1.IPTVProvider {
	p := clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "argon-tv"}}
	p.Spec.Server = "https://panel.example:8080"
	p.Status.Blocks = []clustarrv1.BlockStatus{
		{Start: "1001", DeviceID: "AAAA0001", URI: "http://" + tvAddress + "/hdhr/AAAA0001", Owner: "plex-a"},
		{Start: "1200", DeviceID: "AAAA0002", URI: "http://" + tvAddress + "/hdhr/AAAA0002"},
	}
	return p
}

func tvChannel(name, key, number string, active bool, reason string) clustarrv1.IPTVChannel {
	c := clustarrv1.IPTVChannel{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, ResourceVersion: "5", Generation: 3},
		Spec: clustarrv1.IPTVChannelSpec{ProviderRef: "argon-tv", Key: key, Number: number, Active: active}}
	status := metav1.ConditionFalse
	if reason == clustarrv1.ReasonActive {
		status = metav1.ConditionTrue
	}
	c.Status.Conditions = []metav1.Condition{{Type: clustarrv1.ConditionReady, Status: status, Reason: reason, Message: reason + " message"}}
	return c
}

var playlist = map[string]livetvstore.PlaylistEntry{
	"n:espn": {Key: "n:espn", Name: "ESPN HD", Group: "USA: SPORTS", TvgID: "espn.us", Logo: "https://pl.example/espn.png"},
	"n:cnn":  {Key: "n:cnn", Name: "CNN", Group: "USA: NEWS", Logo: "https://pl.example/cnn.png"},
	"n:hbo":  {Key: "n:hbo", Name: "HBO", Group: "USA: MOVIES"},
}

func tvInputs(channels ...clustarrv1.IPTVChannel) projection.LiveTVInputs {
	return projection.LiveTVInputs{
		Providers: []clustarrv1.IPTVProvider{tvProvider()},
		Channels:  channels,
		Playlist: func(_ types.NamespacedName, key string) (livetvstore.PlaylistEntry, bool) {
			e, ok := playlist[key]
			return e, ok
		},
		GuideLogo: func(_ types.NamespacedName, number string) string {
			if number == "1001" {
				return "https://epg.example/espn.png"
			}
			return ""
		},
	}
}

func TestLiveTVChannelsResolveNamesGroupsAndLogos(t *testing.T) {
	espn := tvChannel("argon-tv-espn", "n:espn", "1001", true, clustarrv1.ReasonActive)
	cnn := tvChannel("argon-tv-cnn", "n:cnn", "1002", true, clustarrv1.ReasonActive)
	cnn.Spec.Name, cnn.Spec.Group, cnn.Spec.Logo = "CNN US", "News", "https://own.example/cnn.png"
	hbo := tvChannel("argon-tv-hbo", "n:hbo", "1010", false, clustarrv1.ReasonInactive)
	rows := projection.LiveTVChannels(tvInputs(espn, cnn, hbo))
	require.Len(t, rows, 3)
	key := types.NamespacedName{Namespace: "media", Name: "argon-tv"}

	assert.Equal(t, "ESPN HD", rows[0].DisplayName, "the playlist's name")
	assert.False(t, rows[0].NameOverridden)
	assert.Equal(t, "USA: SPORTS", rows[0].Group)
	assert.Equal(t, "espn.us", rows[0].TvgID)
	assert.Equal(t, "https://epg.example/espn.png", rows[0].Logo, "an active channel's logo is the one Plex is given")
	assert.Equal(t, projection.LogoPath(key, "https://epg.example/espn.png"), rows[0].LogoPath)

	assert.Equal(t, "CNN US", rows[1].DisplayName, "the override")
	assert.True(t, rows[1].NameOverridden)
	assert.Equal(t, "News", rows[1].Group)
	assert.Equal(t, "https://own.example/cnn.png", rows[1].Logo, "spec.logo when the guide index has none")

	assert.Equal(t, "HBO", rows[2].DisplayName)
	assert.Empty(t, rows[2].Logo, "no logo anywhere")
	assert.Empty(t, rows[2].LogoPath)
	assert.Equal(t, int64(3), rows[2].Generation, "the generation an edit names, never the resourceVersion a status write moves")
	assert.Equal(t, "argon-tv", rows[2].Provider)
	assert.Equal(t, "media", rows[2].Namespace)
}

func TestLiveTVChannelsPlaceActiveChannelsInTheirBlock(t *testing.T) {
	a := tvChannel("a", "n:espn", "1001", true, clustarrv1.ReasonActive)
	b := tvChannel("b", "n:cnn", "1250", true, clustarrv1.ReasonActive)
	off := tvChannel("c", "n:hbo", "1300", false, clustarrv1.ReasonInactive)
	rows := projection.LiveTVChannels(tvInputs(a, b, off))
	assert.Equal(t, "1001", rows[0].BlockStart)
	assert.Equal(t, "plex-a", rows[0].Owner)
	assert.Equal(t, "http://"+tvAddress+"/hdhr/AAAA0001/stream/1001", rows[0].StreamURL)
	assert.Equal(t, "1200", rows[1].BlockStart)
	assert.Equal(t, "http://"+tvAddress+"/hdhr/AAAA0002/stream/1250", rows[1].StreamURL)
	assert.Empty(t, rows[2].StreamURL, "an inactive channel streams nothing")
	assert.Empty(t, rows[2].BlockStart)
}

func TestLiveTVChannelsSortByNumber(t *testing.T) {
	rows := projection.LiveTVChannels(tvInputs(
		tvChannel("ten", "n:hbo", "10", true, clustarrv1.ReasonActive),
		tvChannel("nine-two", "n:cnn", "9.2", true, clustarrv1.ReasonActive),
		tvChannel("nine", "n:espn", "9", true, clustarrv1.ReasonActive),
	))
	var got []string
	for _, r := range rows {
		got = append(got, r.Number)
	}
	assert.Equal(t, []string{"9", "9.2", "10"}, got)
}

func TestAMissingPlaylistEntryReadsMissing(t *testing.T) {
	c := tvChannel("gone", "n:gone", "1003", true, clustarrv1.ReasonMissing)
	rows := projection.LiveTVChannels(tvInputs(c))
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Missing)
	assert.False(t, rows[0].Ready)
	assert.Equal(t, "n:gone", rows[0].DisplayName, "its key, with no name anywhere")
	assert.Empty(t, rows[0].StreamURL)
	assert.Equal(t, "Missing message", rows[0].Message)
}

func TestTheChannelsEPGChoiceIsRead(t *testing.T) {
	auto := tvChannel("auto", "n:espn", "1001", true, clustarrv1.ReasonActive)
	dummy := tvChannel("dummy", "n:cnn", "1002", true, clustarrv1.ReasonActive)
	dummy.Spec.EPG = &clustarrv1.ChannelEPG{Dummy: true}
	dummy.Status.Guide = "dummy/60"
	mapped := tvChannel("mapped", "n:hbo", "1003", true, clustarrv1.ReasonActive)
	mapped.Spec.EPG = &clustarrv1.ChannelEPG{ChannelID: "US-HBO"}
	mapped.Status.Guide = "xmltv/US-HBO"
	rows := projection.LiveTVChannels(tvInputs(auto, dummy, mapped))
	assert.Equal(t, []string{"", "dummy", "US-HBO"}, []string{rows[0].EPG, rows[1].EPG, rows[2].EPG})
	assert.Equal(t, "xmltv/US-HBO", rows[2].Guide)
}

func TestLiveTVSourcesFlagAnAccountExpiringWithinAWeek(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	soon, later := metav1.NewTime(now.Add(3*24*time.Hour)), metav1.NewTime(now.Add(30*24*time.Hour))
	a, b := tvProvider(), tvProvider()
	b.Name = "b-tv"
	a.Status.Account = &clustarrv1.AccountStatus{Status: "Active", ExpiresAt: &soon}
	b.Status.Account = &clustarrv1.AccountStatus{Status: "Active", ExpiresAt: &later}
	a.Status.Tuners = &clustarrv1.TunerStatus{Total: 4, InUse: 1}
	a.Status.Conditions = []metav1.Condition{{Type: clustarrv1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "serving 257 channels"}}
	rows := projection.LiveTVSources([]clustarrv1.IPTVProvider{b, a}, now)
	require.Len(t, rows, 2)
	assert.Equal(t, "argon-tv", rows[0].Name, "by namespace and name")
	assert.True(t, rows[0].ExpiresSoon)
	assert.False(t, rows[1].ExpiresSoon)
	assert.True(t, rows[0].Ready)
	assert.Equal(t, "serving 257 channels", rows[0].Message)
	assert.Equal(t, int32(4), rows[0].TunersTotal)
	assert.Equal(t, int32(1), rows[0].TunersInUse)
	require.Len(t, rows[0].Blocks, 2)
	assert.Equal(t, "http://"+tvAddress+"/hdhr/AAAA0001", rows[0].Blocks[0].URI)
	assert.True(t, rows[0].Enabled)
}

func TestTheGuideSourceNeverCarriesTheAccount(t *testing.T) {
	p := tvProvider()
	rows := projection.LiveTVSources([]clustarrv1.IPTVProvider{p}, time.Now())
	assert.Equal(t, "https://panel.example:8080/xmltv.php", rows[0].GuideSource)
	assert.False(t, rows[0].GuideFallback)
	p.Spec.GuideURL = "https://epg.example/{username}/guide.xml?key={password}"
	rows = projection.LiveTVSources([]clustarrv1.IPTVProvider{p}, time.Now())
	assert.Equal(t, "https://epg.example/{username}/guide.xml?key={password}", rows[0].GuideSource, "as written")
	assert.True(t, rows[0].GuideFallback, "the panel's guide fills the channels it lacks")
	p.Spec.EPGSource = clustarrv1.EPGSourcePMS
	rows = projection.LiveTVSources([]clustarrv1.IPTVProvider{p}, time.Now())
	assert.True(t, rows[0].GuidePMS)
}

func TestBlockOfIsTheLastBlockStartingAtOrBelow(t *testing.T) {
	bs := tvProvider().Status.Blocks
	b, ok := projection.BlockOf(bs, "1199")
	require.True(t, ok)
	assert.Equal(t, "1001", b.Start)
	b, _ = projection.BlockOf(bs, "1200")
	assert.Equal(t, "1200", b.Start)
	_, ok = projection.BlockOf(bs, "999")
	assert.False(t, ok)
}

func TestLiveTVSourcesShowAnM3UProvidersType(t *testing.T) {
	p := tvProvider()
	p.Spec.Type, p.Spec.Server = clustarrv1.ProviderTypeM3U, ""
	p.Spec.PlaylistURL = "https://lists.example/{username}/all.m3u?key={password}"
	rows := projection.LiveTVSources([]clustarrv1.IPTVProvider{p}, time.Now())
	assert.Equal(t, "M3U", rows[0].Type)
	assert.Equal(t, "lists.example", rows[0].Server, "the playlist URL's host, no path or query")
	assert.Empty(t, rows[0].GuideSource, "no guideURL, no panel: no guide")
	p.Spec.GuideURL = "https://epg.example/guide.xml"
	rows = projection.LiveTVSources([]clustarrv1.IPTVProvider{p}, time.Now())
	assert.Equal(t, "https://epg.example/guide.xml", rows[0].GuideSource)
	assert.False(t, rows[0].GuideFallback, "no panel guide to fall back to")
}
