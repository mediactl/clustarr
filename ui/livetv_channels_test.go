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

package ui_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

var tvKey = types.NamespacedName{Namespace: "media", Name: "argon-tv"}

func tvChannel(name, key, number string, active bool, reason, guide string) *clustarrv1.IPTVChannel {
	c := &clustarrv1.IPTVChannel{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name},
		Spec: clustarrv1.IPTVChannelSpec{ProviderRef: "argon-tv", Key: key, Number: number, Active: active}}
	status := metav1.ConditionFalse
	if reason == clustarrv1.ReasonActive {
		status = metav1.ConditionTrue
	}
	c.Status.Conditions = []metav1.Condition{{Type: clustarrv1.ConditionReady, Status: status, Reason: reason, Message: reason, LastTransitionTime: metav1.NewTime(tvNow)}}
	c.Status.Guide = guide
	return c
}

func putObject(t *testing.T, store events.ObjectStore, name string, v any) {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, livetvstore.Encode(&b, v))
	_, err := store.Put(context.Background(), name, &b, events.ObjectMeta{})
	require.NoError(t, err)
}

// channelsFixture is argon-tv with four channels -- 1001 mapped to a guide
// channel, 1002 on a dummy guide, 1003 whose key the playlist lost, 1010
// inactive -- a 120-entry playlist, 13 guide channels and a guide index.
func channelsFixture(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	p := tvProvider()
	p.Spec.Filters = []clustarrv1.ChannelFilter{
		{Name: "sports", Type: clustarrv1.FilterType("groupTitle"), Match: "USA: SPORTS"},
		{Name: "news", Type: clustarrv1.FilterType("groupTitle"), Match: "USA: NEWS"},
		{Name: "ent", Type: clustarrv1.FilterType("groupTitle"), Match: "USA: ENTERTAINMENT"},
	}
	espn := tvChannel("argon-tv-espn", "n:espn", "1001", true, clustarrv1.ReasonActive, "xmltv/US-ESPN")
	espn.Spec.EPG = &clustarrv1.ChannelEPG{ChannelID: "US-ESPN"}
	cnn := tvChannel("argon-tv-cnn", "n:cnn", "1002", true, clustarrv1.ReasonActive, "dummy/60")
	cnn.Spec.EPG = &clustarrv1.ChannelEPG{Dummy: true}
	gone := tvChannel("argon-tv-gone", "n:gone", "1003", true, clustarrv1.ReasonMissing, "")
	hbo := tvChannel("argon-tv-hbo", "n:hbo", "1010", false, clustarrv1.ReasonInactive, "")

	store := liveTVStore(t)
	pl := livetvstore.Playlist{Entries: []livetvstore.PlaylistEntry{
		{Key: "n:espn", Name: "ESPN HD", Group: "USA: SPORTS", Logo: "https://pl.example/espn.png", URL: "http://provider.example/live/{username}/{password}/1.ts"},
		{Key: "n:cnn", Name: "CNN", Group: "USA: NEWS", Logo: "https://pl.example/cnn.png", URL: "http://provider.example/live/{username}/{password}/2.ts"},
		{Key: "n:hbo", Name: "HBO", Group: "USA: MOVIES", URL: "http://provider.example/live/{username}/{password}/3.ts"},
	}}
	for i := range 117 {
		pl.Entries = append(pl.Entries, livetvstore.PlaylistEntry{Key: fmt.Sprintf("n:c%03d", i), Name: fmt.Sprintf("Channel %03d", i), Group: "USA: ENTERTAINMENT",
			URL: "http://provider.example/live/{username}/{password}/x.ts"})
	}
	putObject(t, store, livetvstore.PlaylistObject(tvKey), pl)
	gc := []livetvstore.GuideChannel{
		{ID: "US-ESPN", DisplayName: "ESPN"}, {ID: "US-ESPN2", DisplayName: "ESPN 2"}, {ID: "US-ESPNEWS", DisplayName: "ESPNews"},
		{ID: "US-FOXESPN", DisplayName: "Fox ESPN Deportes"}, {ID: "US-CNN", DisplayName: "CNN"},
	}
	for i := range 8 {
		gc = append(gc, livetvstore.GuideChannel{ID: fmt.Sprintf("US-X%d", i), DisplayName: fmt.Sprintf("Other %d", i)})
	}
	putObject(t, store, livetvstore.GuideChannelsObject(tvKey), gc)
	putObject(t, store, livetvstore.GuideIndexObject(tvKey), livetvstore.GuideIndex{
		Channels: []livetvstore.IndexChannel{{Number: "1001", Name: "ESPN HD", Logo: "https://epg.example/espn.png"}, {Number: "1002", Name: "CNN"}},
	})
	return tvServerWith(t, store, p, espn, cnn, gone, hbo)
}

func channelRowIDs(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<tr id="channel-([a-z0-9-]+)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func getChannel(t *testing.T, c client.Client, name string) *clustarrv1.IPTVChannel {
	t.Helper()
	var ch clustarrv1.IPTVChannel
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: name}, &ch))
	return &ch
}

// postTo posts form to path as htmx aimed at target.
func postTo(t *testing.T, srv *ui.Server, path, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", target)
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestChannelsPageListsChannelsByNumberWithTheirResolvedFields(t *testing.T) {
	srv, _ := channelsFixture(t)
	rec := get(t, srv, "/livetv/channels")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Equal(t, []string{"argon-tv-espn", "argon-tv-cnn", "argon-tv-gone", "argon-tv-hbo"}, channelRowIDs(body))
	assert.Contains(t, body, "ESPN HD")
	assert.Contains(t, body, "USA: SPORTS")
	requireTag(t, body, `src="`+projection.LogoPath(tvKey, "https://epg.example/espn.png")+`"`, "data-logo")
	espn := body[strings.Index(body, `id="channel-argon-tv-espn"`):strings.Index(body, `id="channel-argon-tv-cnn"`)]
	assert.Contains(t, espn, "US-ESPN")
	assert.Contains(t, espn, "1001+")
	assert.Contains(t, espn, "plex-a")
	cnn := body[strings.Index(body, `id="channel-argon-tv-cnn"`):strings.Index(body, `id="channel-argon-tv-gone"`)]
	assert.Contains(t, cnn, "Dummy")
	gone := body[strings.Index(body, `id="channel-argon-tv-gone"`):strings.Index(body, `id="channel-argon-tv-hbo"`)]
	assert.Contains(t, gone, "Missing")
	requireTag(t, body, `href="/settings/edit/iptvchannels/media/argon-tv-espn?return=/livetv/channels"`)
	assert.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"))
}

func TestChannelsFiltersNarrowTheTable(t *testing.T) {
	srv, _ := channelsFixture(t)
	for q, want := range map[string][]string{
		"q=espn":                         {"argon-tv-espn"},
		"q=101":                          {"argon-tv-hbo"},
		"group=USA%3A+NEWS":              {"argon-tv-cnn"},
		"state=inactive":                 {"argon-tv-hbo"},
		"state=active":                   {"argon-tv-espn", "argon-tv-cnn", "argon-tv-gone"},
		"noguide=1":                      {"argon-tv-cnn", "argon-tv-gone", "argon-tv-hbo"},
		"nologo=1":                       {"argon-tv-gone", "argon-tv-hbo"},
		"provider=media%2Fargon-tv&q=cn": {"argon-tv-cnn"},
		"provider=media%2Fother":         nil,
	} {
		body := get(t, srv, "/livetv/channels/table?"+q).Body.String()
		assert.Equal(t, want, channelRowIDs(body), q)
	}
}

func TestAnInPlaceEditPatchesTheChannelAndAnswersItsRow(t *testing.T) {
	srv, c := channelsFixture(t)
	rv := getChannel(t, c, "argon-tv-espn").ResourceVersion
	rec := postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"rv": {rv}, "number": {"1005"}})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("HX-Retarget"))
	body := rec.Body.String()
	assert.Equal(t, []string{"argon-tv-espn"}, channelRowIDs(body), "the row alone")
	assert.Contains(t, body, `value="1005"`)
	assert.Equal(t, "1005", getChannel(t, c, "argon-tv-espn").Spec.Number)

	rv = getChannel(t, c, "argon-tv-espn").ResourceVersion
	rec = postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"rv": {rv}, "epg": {""}})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, getChannel(t, c, "argon-tv-espn").Spec.EPG, "automatic")
}

func TestAnEditThatLostARaceSaysSoInPlace(t *testing.T) {
	srv, c := channelsFixture(t)
	rec := postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"rv": {"1"}, "number": {"1005"}})
	require.Equal(t, http.StatusOK, rec.Code, "htmx swaps a 200 only")
	assert.Equal(t, "#action-status", rec.Header().Get("HX-Retarget"))
	assert.Equal(t, "innerHTML", rec.Header().Get("HX-Reswap"))
	requireTag(t, rec.Body.String(), `data-action-error="conflict"`)
	assert.Equal(t, "1001", getChannel(t, c, "argon-tv-espn").Spec.Number, "nothing written over the other edit")
}

func TestBulkRenumberChecksEveryItemBeforeWriting(t *testing.T) {
	srv, c := channelsFixture(t)
	rv := getChannel(t, c, "argon-tv-espn").ResourceVersion
	rec := postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{
		"item": {"media/argon-tv-espn@" + rv, "media/nope@1"}, "op": {"renumber"}, "start": {"2001"},
	})
	requireTag(t, rec.Body.String(), `data-action-error="invalid"`)
	assert.Equal(t, "1001", getChannel(t, c, "argon-tv-espn").Spec.Number)
	rec = postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{
		"item": {"media/argon-tv-espn@" + rv}, "op": {"renumber"}, "start": {"20a"},
	})
	requireTag(t, rec.Body.String(), `data-action-error="invalid"`)
}

func TestBulkRenumberNumbersInTableOrder(t *testing.T) {
	srv, c := channelsFixture(t)
	espn, cnn := getChannel(t, c, "argon-tv-espn"), getChannel(t, c, "argon-tv-cnn")
	rec := postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{
		"item": {"media/argon-tv-cnn@" + cnn.ResourceVersion, "media/argon-tv-espn@" + espn.ResourceVersion}, "op": {"renumber"}, "start": {"2001"},
	})
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	assert.Equal(t, "channels-changed", rec.Header().Get("HX-Trigger"))
	assert.Equal(t, "2001", getChannel(t, c, "argon-tv-espn").Spec.Number, "1001 came first")
	assert.Equal(t, "2002", getChannel(t, c, "argon-tv-cnn").Spec.Number)

	hbo := getChannel(t, c, "argon-tv-hbo")
	rec = postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{"item": {"media/argon-tv-hbo@" + hbo.ResourceVersion}, "op": {"enable"}})
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	assert.True(t, getChannel(t, c, "argon-tv-hbo").Spec.Active)
	hbo = getChannel(t, c, "argon-tv-hbo")
	rec = postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{"item": {"media/argon-tv-hbo@" + hbo.ResourceVersion}, "op": {"group"}, "value": {"Movies"}})
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	assert.Equal(t, "Movies", getChannel(t, c, "argon-tv-hbo").Spec.Group)
	hbo = getChannel(t, c, "argon-tv-hbo")
	rec = postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{"item": {"media/argon-tv-hbo@" + hbo.ResourceVersion}, "op": {"delete"}})
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	var gone clustarrv1.IPTVChannel
	assert.Error(t, c.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "argon-tv-hbo"}, &gone))
}

func TestDeletingAChannelRemovesItsRow(t *testing.T) {
	srv, c := channelsFixture(t)
	rec := postTo(t, srv, "/livetv/channels/media/argon-tv-hbo/delete", "channel-argon-tv-hbo", url.Values{})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, strings.TrimSpace(rec.Body.String()), "an empty answer removes the row")
	var gone clustarrv1.IPTVChannel
	assert.Error(t, c.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "argon-tv-hbo"}, &gone))
}

func TestThePlaylistPanePagesAndSearchesServerSide(t *testing.T) {
	srv, _ := channelsFixture(t)
	body := get(t, srv, "/livetv/channels/playlist?provider=media%2Fargon-tv&all=1").Body.String()
	assert.Equal(t, 50, strings.Count(body, "data-playlist-entry="), "a page of 50")
	assert.Contains(t, body, "120", "the total")
	requireTag(t, body, `hx-get="/livetv/channels/playlist?all=1&amp;page=2&amp;provider=media%2Fargon-tv"`)
	assert.NotContains(t, body, "{username}")
	assert.NotContains(t, body, "provider.example", "no stream URL")

	body = get(t, srv, "/livetv/channels/playlist?provider=media%2Fargon-tv&all=1&q=espn").Body.String()
	assert.Equal(t, 1, strings.Count(body, "data-playlist-entry="))
	entry := tagWith(t, body, `data-playlist-entry="n:espn"`)
	require.NotEmpty(t, entry)
	assert.Contains(t, body, "1001", "the channel it already is")

	body = get(t, srv, "/livetv/channels/playlist?provider=media%2Fargon-tv&q=hbo").Body.String()
	assert.Zero(t, strings.Count(body, "data-playlist-entry="), "not a candidate: the provider's filters leave it out")
	body = get(t, srv, "/livetv/channels/playlist?provider=media%2Fargon-tv&all=1&q=hbo").Body.String()
	assert.Contains(t, body, "1010")
	assert.Contains(t, body, "sports", "the provider's filters as chips")
}

func TestAddAsChannelTakesTheNextFreeNumber(t *testing.T) {
	srv, c := channelsFixture(t)
	rec := postTo(t, srv, "/livetv/channels/add", "pl-x", url.Values{"provider": {"media/argon-tv"}, "key": {"n:c005"}, "name": {"Channel 005"}})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "channels-changed", rec.Header().Get("HX-Trigger"))
	var list clustarrv1.IPTVChannelList
	require.NoError(t, c.List(t.Context(), &list))
	var added *clustarrv1.IPTVChannel
	for i := range list.Items {
		if list.Items[i].Spec.Key == "n:c005" {
			added = &list.Items[i]
		}
	}
	require.NotNil(t, added)
	assert.Equal(t, "1011", added.Spec.Number, "one past 1010")
	assert.True(t, added.Spec.Active)
	assert.Contains(t, rec.Body.String(), "1011")

	rec = postTo(t, srv, "/livetv/channels/add", "pl-x", url.Values{"provider": {"media/argon-tv"}, "key": {"n:c005"}, "name": {"Channel 005"}})
	assert.Contains(t, rec.Body.String(), "1011", "added once: the row names its channel")

	empty := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "empty-tv"}}
	require.NoError(t, c.Create(t.Context(), empty))
	rec = postTo(t, srv, "/livetv/channels/add", "pl-y", url.Values{"provider": {"media/empty-tv"}, "key": {"n:x"}, "name": {"X"}})
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, c.List(t.Context(), &list))
	for _, ch := range list.Items {
		if ch.Spec.ProviderRef == "empty-tv" {
			assert.Equal(t, "1000", ch.Spec.Number, "an empty provider starts at its channelNumberStart")
		}
	}
}

func TestTheEPGPickerRanksTheGuidesChannels(t *testing.T) {
	srv, _ := channelsFixture(t)
	body := get(t, srv, "/livetv/channels/media/argon-tv-espn/epg?q=espn").Body.String()
	var ids []string
	for _, m := range regexp.MustCompile(`data-epg-choice="([^"]*)"`).FindAllStringSubmatch(body, -1) {
		ids = append(ids, m[1])
	}
	require.GreaterOrEqual(t, len(ids), 6)
	assert.Equal(t, []string{"", "dummy", "US-ESPN"}, ids[:3], "automatic, dummy, then the exact match")
	assert.ElementsMatch(t, []string{"US-ESPN2", "US-ESPNEWS"}, ids[3:5], "then the prefixes")
	assert.Equal(t, "US-FOXESPN", ids[5], "then the substring")
	assert.LessOrEqual(t, len(ids), 22)

	body = get(t, srv, "/livetv/channels/media/argon-tv-espn/epg").Body.String()
	assert.Contains(t, body, `value="ESPN HD"`, "the search starts at the channel's name")
}

func TestCopyStreamURLIsTheTunersNeverTheProviders(t *testing.T) {
	srv, _ := channelsFixture(t)
	body := get(t, srv, "/livetv/channels").Body.String()
	requireTag(t, body, `data-copy="http://`+tvAddr+`/hdhr/AAAA0001/stream/1001"`)
	assert.NotContains(t, body, "provider.example")
	assert.NotContains(t, body, "panel.example")
	requireTag(t, body, `data-copy="http://`+tvAddr+`/hdhr/AAAA0001/playlist.m3u"`)
	requireTag(t, body, `data-copy="http://`+tvAddr+`/hdhr/AAAA0001/xmltv.xml"`)
}
