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
	"encoding/json"
	"fmt"
	"html"
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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/iptv/lineup"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

var tvKey = types.NamespacedName{Namespace: "media", Name: "argon-tv"}

func tvChannel(name, key, number string, active bool, reason, guide string) *clustarrv1.IPTVChannel {
	c := &clustarrv1.IPTVChannel{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, Generation: 1},
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
	rec := postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"gen": {"1"}, "number": {"1005"}})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("HX-Retarget"))
	body := rec.Body.String()
	assert.Equal(t, []string{"argon-tv-espn"}, channelRowIDs(body), "the row alone")
	assert.Contains(t, body, `value="1005"`)
	assert.Equal(t, "1005", getChannel(t, c, "argon-tv-espn").Spec.Number)

	rec = postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"gen": {"1"}, "epg": {""}})
	require.Equal(t, http.StatusOK, rec.Code)
	if epg := getChannel(t, c, "argon-tv-espn").Spec.EPG; epg != nil {
		// Automatic clears channelID and dummy and keeps spec.epg.plex, so
		// an empty spec.epg may stay: the same as none, mapped by tvg-id.
		assert.Empty(t, epg.ChannelID, "automatic")
		assert.False(t, epg.Dummy, "automatic")
	}
}

// rowVals are the hx-vals the first in-place control of a row the server
// answered posts with: what the page holds after an edit.
func rowVals(t *testing.T, body string) url.Values {
	t.Helper()
	m := regexp.MustCompile(`hx-vals="([^"]*)"`).FindStringSubmatch(body)
	require.NotNil(t, m, "the row posts its edits with hx-vals")
	var kv map[string]string
	require.NoError(t, json.Unmarshal([]byte(html.UnescapeString(m[1])), &kv))
	out := url.Values{}
	for k, v := range kv {
		if k != "active" {
			out.Set(k, v)
		}
	}
	return out
}

func TestASecondEditOfARowAfterItsStatusWasWrittenIsNoConflict(t *testing.T) {
	srv, c := channelsFixture(t)
	rec := postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"gen": {"1"}, "number": {"1005"}})
	require.Empty(t, rec.Header().Get("HX-Retarget"), rec.Body.String())
	vals := rowVals(t, rec.Body.String())

	// The channel controller writes status after every spec edit, which
	// moves resourceVersion and not the spec the page read (the fake has
	// no status subresource, so the write is a whole update).
	ch := getChannel(t, c, "argon-tv-espn")
	before := ch.ResourceVersion
	ch.Status.ObservedGeneration = ch.Generation
	require.NoError(t, c.Update(t.Context(), ch))
	require.NotEqual(t, before, getChannel(t, c, "argon-tv-espn").ResourceVersion)

	vals.Set("name", "ESPN East")
	rec = postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", vals)
	assert.Empty(t, rec.Header().Get("HX-Retarget"), rec.Body.String())
	assert.Equal(t, "ESPN East", getChannel(t, c, "argon-tv-espn").Spec.Name)
}

func TestAnEditThatLostARaceSaysSoInPlace(t *testing.T) {
	srv, c := channelsFixture(t)
	ch := getChannel(t, c, "argon-tv-espn")
	ch.Spec.Number, ch.Generation = "1007", 2 // kubectl's edit, after the page loaded
	require.NoError(t, c.Update(t.Context(), ch))

	rec := postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"gen": {"1"}, "number": {"1005"}})
	require.Equal(t, http.StatusOK, rec.Code, "htmx swaps a 200 only")
	assert.Equal(t, "#action-status", rec.Header().Get("HX-Retarget"))
	assert.Equal(t, "innerHTML", rec.Header().Get("HX-Reswap"))
	requireTag(t, rec.Body.String(), `data-action-error="conflict"`)
	assert.Equal(t, "1007", getChannel(t, c, "argon-tv-espn").Spec.Number, "nothing written over the other edit")
}

func TestTheEPGPickerSwitchesBetweenAGuideChannelAndADummy(t *testing.T) {
	srv, c := channelsFixture(t)
	rec := postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"gen": {"1"}, "epg": {"dummy"}})
	require.Empty(t, rec.Header().Get("HX-Retarget"), rec.Body.String())
	assert.Equal(t, &clustarrv1.ChannelEPG{Dummy: true}, getChannel(t, c, "argon-tv-espn").Spec.EPG, "the guide channel is cleared")

	ch := getChannel(t, c, "argon-tv-cnn")
	ch.Spec.EPG.DummyMinutes = ptr.To[int32](30)
	require.NoError(t, c.Update(t.Context(), ch))
	rec = postTo(t, srv, "/livetv/channels/media/argon-tv-cnn", "channel-argon-tv-cnn", url.Values{"gen": {"1"}, "epg": {"US-CNN"}})
	require.Empty(t, rec.Header().Get("HX-Retarget"), rec.Body.String())
	assert.Equal(t, &clustarrv1.ChannelEPG{ChannelID: "US-CNN"}, getChannel(t, c, "argon-tv-cnn").Spec.EPG, "the dummy and its minutes are cleared")
}

func TestBulkRenumberChecksEveryItemBeforeWriting(t *testing.T) {
	srv, c := channelsFixture(t)
	rec := postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{
		"item": {"media/argon-tv-espn@1", "media/nope@1"}, "op": {"renumber"}, "start": {"2001"},
	})
	requireTag(t, rec.Body.String(), `data-action-error="invalid"`)
	assert.Equal(t, "1001", getChannel(t, c, "argon-tv-espn").Spec.Number)
	rec = postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{
		"item": {"media/argon-tv-espn@1"}, "op": {"renumber"}, "start": {"20a"},
	})
	requireTag(t, rec.Body.String(), `data-action-error="invalid"`)
}

func TestBulkRenumberNumbersInTableOrder(t *testing.T) {
	srv, c := channelsFixture(t)
	rec := postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{
		"item": {"media/argon-tv-cnn@1", "media/argon-tv-espn@1"}, "op": {"renumber"}, "start": {"2001"},
	})
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	assert.Equal(t, "channels-changed", rec.Header().Get("HX-Trigger"))
	assert.Equal(t, "2001", getChannel(t, c, "argon-tv-espn").Spec.Number, "1001 came first")
	assert.Equal(t, "2002", getChannel(t, c, "argon-tv-cnn").Spec.Number)

	rec = postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{"item": {"media/argon-tv-hbo@1"}, "op": {"enable"}})
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	assert.True(t, getChannel(t, c, "argon-tv-hbo").Spec.Active)
	rec = postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{"item": {"media/argon-tv-hbo@1"}, "op": {"group"}, "value": {"Movies"}})
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	assert.Equal(t, "Movies", getChannel(t, c, "argon-tv-hbo").Spec.Group)
}

func TestBulkDeleteAsksBeforeItDeletes(t *testing.T) {
	srv, c := channelsFixture(t)
	rec := postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{"item": {"media/argon-tv-hbo@1", "media/argon-tv-gone@1"}, "op": {"delete"}})
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	confirm := tagWith(t, body, `data-action-confirm="delete-channels"`)
	assert.Contains(t, body, "Delete 2 channels?")
	assert.Contains(t, confirm, `hx-post="/livetv/channels/bulk"`)
	requireTag(t, body, `name="confirm" value="yes"`)
	requireTag(t, body, `name="item" value="media/argon-tv-hbo@1"`)
	requireTag(t, body, `name="item" value="media/argon-tv-gone@1"`)
	assert.Empty(t, rec.Header().Get("HX-Trigger"))
	getChannel(t, c, "argon-tv-hbo") // still there

	rec = postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{"item": {"media/argon-tv-hbo@1", "media/argon-tv-gone@1"}, "op": {"delete"}, "confirm": {"yes"}})
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	assert.Equal(t, "channels-changed", rec.Header().Get("HX-Trigger"))
	for _, name := range []string{"argon-tv-hbo", "argon-tv-gone"} {
		var gone clustarrv1.IPTVChannel
		assert.Error(t, c.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: name}, &gone), name)
	}
}

func TestBulkDeleteRefusesAChannelEditedSinceThePageRead(t *testing.T) {
	srv, c := channelsFixture(t)
	ch := getChannel(t, c, "argon-tv-hbo")
	ch.Spec.Number, ch.Generation = "1011", 2
	require.NoError(t, c.Update(t.Context(), ch))
	rec := postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{"item": {"media/argon-tv-gone@1", "media/argon-tv-hbo@1"}, "op": {"delete"}, "confirm": {"yes"}})
	requireTag(t, rec.Body.String(), `data-action-error="conflict"`)
	getChannel(t, c, "argon-tv-hbo")
	getChannel(t, c, "argon-tv-gone") // none deleted
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

// lineupFixture is argon-tv under an automatic lineup (enabled, or a dry run
// when not): News (base 1000) and Sports (base 2000) in spec.lineup and, when
// enabled, status.categories, and four channels the lineup placed -- CNN in
// News by the person's override, ESPN in Sports on a Plex grid, ESPN2 left
// out as ESPN's duplicate, and an Off one -- with plan, when set, stored as
// the provider's lineup plan.
func lineupFixture(t *testing.T, enabled bool, plan *livetvstore.LineupPlan) (*ui.Server, client.Client) {
	t.Helper()
	p := tvProvider()
	p.Spec.Lineup = &clustarrv1.LineupSpec{Enabled: enabled, Categories: []clustarrv1.LineupCategory{
		{Name: "News", Base: ptr.To[int32](1000), Groups: []string{"USA: NEWS"}},
		{Name: "Sports", Base: ptr.To[int32](2000), Groups: []string{"USA: SPORTS"}},
	}}
	if enabled {
		p.Status.Categories = []clustarrv1.CategoryStatus{{Name: "News", Base: 1000, Channels: 1, DeviceID: "BBBB0001"}, {Name: "Sports", Base: 2000, Channels: 1, DeviceID: "BBBB0002"}}
	}
	cnn := tvChannel("argon-tv-cnn", "n:cnn", "1001", true, clustarrv1.ReasonActive, "xmltv/US-CNN")
	cnn.Spec.Category = ptr.To("News")
	cnn.Status.Lineup = &clustarrv1.ChannelLineupStatus{Name: "CNN", Category: "News", Rule: "override", Number: "1001"}
	espn := tvChannel("argon-tv-espn", "n:espn", "2001", true, clustarrv1.ReasonActive, "plex/5fc705e2088e7d002d4c6de2")
	espn.Status.Lineup = &clustarrv1.ChannelLineupStatus{Name: "ESPN", Category: "Sports", Rule: "Sports[1]", Number: "2001"}
	espn2 := tvChannel("argon-tv-espn2", "n:espn2", "2050", false, clustarrv1.ReasonInactive, "")
	espn2.Status.Lineup = &clustarrv1.ChannelLineupStatus{Name: "ESPN", DuplicateOf: "n:espn"}
	off := tvChannel("argon-tv-shop", "n:shop", "2051", false, clustarrv1.ReasonInactive, "dummy/60")
	off.Spec.Category = ptr.To("")
	off.Status.Lineup = &clustarrv1.ChannelLineupStatus{Name: "Shop"}

	store := liveTVStore(t)
	putObject(t, store, livetvstore.PlaylistObject(tvKey), livetvstore.Playlist{Entries: []livetvstore.PlaylistEntry{
		{Key: "n:cnn", Name: "CNN", Group: "USA: NEWS"},
		{Key: "n:espn", Name: "ESPN HD", Group: "USA: SPORTS"},
		{Key: "n:espn2", Name: "ESPN [720p]", Group: "USA: SPORTS"},
		{Key: "n:shop", Name: "Shop", Group: "USA: SHOPPING"},
		{Key: "n:hbo", Name: "HBO", Group: "USA: MOVIES"},
	}})
	if plan != nil {
		putObject(t, store, livetvstore.LineupPlanObject(tvKey), *plan)
	}
	return tvServerWith(t, store, p, cnn, espn, espn2, off)
}

// rowOf is channel name's row in body, through the next row or the table's
// end.
func rowOf(t *testing.T, body, name string) string {
	t.Helper()
	i := strings.Index(body, `id="channel-`+name+`"`)
	require.GreaterOrEqual(t, i, 0, name)
	end := strings.Index(body[i+1:], `<tr id="channel-`)
	if end < 0 {
		end = strings.Index(body[i:], "</tbody>")
		require.Greater(t, end, 0)
		return body[i : i+end]
	}
	return body[i : i+1+end]
}

func TestTheChannelsPageShowsCategoryAndSource(t *testing.T) {
	srv, c := lineupFixture(t, true, nil)
	body := get(t, srv, "/livetv/channels").Body.String()
	requireTag(t, body, `data-column="category"`)
	requireTag(t, body, `data-column="source"`)

	espn := rowOf(t, body, "argon-tv-espn")
	assert.Contains(t, tagWith(t, espn, `data-channel-category`), `data-channel-category="Sports"`, "the category the lineup gave it")
	assert.Contains(t, espn, "rule Sports[1]", "the rule that placed it")
	assert.Contains(t, tagWith(t, espn, `data-channel-source`), `data-channel-source="plex"`)
	assert.Contains(t, espn, "Plex", "the guide source in use")
	requireTag(t, espn, `name="name"`, `value="ESPN"`) // the lineup's trimmed name, which the tuner serves, not the playlist's "ESPN HD"
	// The override is a trigger loading its picker on demand: no select,
	// no component script, in a row.
	trigger := requireTag(t, espn, `hx-get="/livetv/channels/media/argon-tv-espn/category"`, `hx-target="#epg-picker"`)
	assert.Contains(t, espn[strings.Index(espn, trigger):], ">Automatic<", "the trigger reads the choice")
	for _, absent := range []string{`name="category"`, `data-slot="select"`, `data-slot="select-trigger"`, "data-templ-value"} {
		assert.NotContains(t, espn, absent)
	}

	cnn := rowOf(t, body, "argon-tv-cnn")
	trigger = requireTag(t, cnn, `hx-get="/livetv/channels/media/argon-tv-cnn/category"`)
	assert.Contains(t, cnn[strings.Index(cnn, trigger):], ">News<", "the override's category")
	assert.Contains(t, cnn, "by its override", "a person's category, not a rule's")
	assert.Contains(t, tagWith(t, cnn, `data-channel-source`), `data-channel-source="xmltv"`)

	dup := rowOf(t, body, "argon-tv-espn2")
	assert.Contains(t, dup, "duplicate of 2001 ESPN", "left out as a duplicate, with the channel it duplicates")

	shop := rowOf(t, body, "argon-tv-shop")
	trigger = requireTag(t, shop, `hx-get="/livetv/channels/media/argon-tv-shop/category"`)
	assert.Contains(t, shop[strings.Index(shop, trigger):], ">Off<")
	assert.Contains(t, tagWith(t, shop, `data-channel-source`), `data-channel-source="dummy"`)

	// The picker's Save posts the override in place.
	rec := postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"gen": {"1"}, "category": {"Off"}})
	require.Empty(t, rec.Header().Get("HX-Retarget"), rec.Body.String())
	assert.Equal(t, ptr.To(""), getChannel(t, c, "argon-tv-espn").Spec.Category, "off every DVR")
	rec = postTo(t, srv, "/livetv/channels/media/argon-tv-cnn", "channel-argon-tv-cnn", url.Values{"gen": {"1"}, "category": {"Automatic"}})
	require.Empty(t, rec.Header().Get("HX-Retarget"), rec.Body.String())
	assert.Nil(t, getChannel(t, c, "argon-tv-cnn").Spec.Category, "the override removed")

	// A page without a lineup is as it was.
	plain, _ := channelsFixture(t)
	body = get(t, plain, "/livetv/channels").Body.String()
	for _, absent := range []string{`data-column="category"`, `data-column="source"`, "data-channel-category", "/category\"", "data-lineup-dry-run"} {
		assert.NotContains(t, body, absent)
	}
	assert.NotContains(t, get(t, plain, "/livetv/channels/playlist?provider=media%2Fargon-tv&all=1").Body.String(), "data-plan-")
}

// TestTheCategoryPickerListsTheChoices is a channel's category override
// loaded on demand (GET .../category, into #epg-picker): shadcn-templ's
// select holding the current choice, offering Automatic, Off and the
// provider's categories, and a Save posting it with the generation read,
// answered by the row and closing the dialog.
func TestTheCategoryPickerListsTheChoices(t *testing.T) {
	srv, _ := lineupFixture(t, true, nil)
	rec := get(t, srv, "/livetv/channels/media/argon-tv-cnn/category")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	requireTag(t, body, `data-slot="dialog-content"`)
	requireTag(t, body, `name="category"`, `value="News"`)
	var choices []string
	for _, m := range regexp.MustCompile(`data-slot="select-item" data-templ-value="([^"]*)"`).FindAllStringSubmatch(body, -1) {
		choices = append(choices, m[1])
	}
	assert.Equal(t, []string{"Automatic", "Off", "News", "Sports"}, choices)
	save := requireTag(t, body, `data-category-save`, `hx-post="/livetv/channels/media/argon-tv-cnn"`, `hx-include="closest form"`,
		`hx-target="#channel-argon-tv-cnn"`, `hx-swap="outerHTML"`, `data-templ-dialog-close="category-picker-argon-tv-cnn"`)
	assert.Contains(t, html.UnescapeString(save), `{"gen":"1"}`)
	requireTag(t, body, `data-category-form`)

	espn := get(t, srv, "/livetv/channels/media/argon-tv-espn/category").Body.String()
	requireTag(t, espn, `name="category"`, `value="Automatic"`)

	plain, _ := channelsFixture(t)
	assert.Equal(t, http.StatusNotFound, get(t, plain, "/livetv/channels/media/argon-tv-espn/category").Code, "no lineup, no category")
	assert.Equal(t, http.StatusNotFound, get(t, srv, "/livetv/channels/media/nope/category").Code)
}

func TestANumberCannotBeEditedUnderAnAutomaticLineup(t *testing.T) {
	srv, c := lineupFixture(t, true, nil)
	body := get(t, srv, "/livetv/channels").Body.String()
	number := tagWith(t, rowOf(t, body, "argon-tv-espn"), `name="number"`)
	assert.Regexp(t, disabledAttr, number, "the lineup numbers it")
	assert.NotContains(t, number, "hx-post", "and nothing posts it")

	rec := postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"gen": {"1"}, "number": {"2005"}})
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("HX-Retarget"), rec.Body.String())
	assert.Equal(t, []string{"argon-tv-espn"}, channelRowIDs(rec.Body.String()), "the row, as it stands")
	assert.Equal(t, "2001", getChannel(t, c, "argon-tv-espn").Spec.Number, "a posted number is not written")

	rec = postTo(t, srv, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"gen": {"1"}, "number": {"2005"}, "name": {"ESPN East"}})
	require.Empty(t, rec.Header().Get("HX-Retarget"), rec.Body.String())
	got := getChannel(t, c, "argon-tv-espn")
	assert.Equal(t, "ESPN East", got.Spec.Name, "the rest of the edit is written")
	assert.Equal(t, "2001", got.Spec.Number)

	rec = postTo(t, srv, "/livetv/channels/bulk", "action-status", url.Values{"item": {"media/argon-tv-cnn@1"}, "op": {"renumber"}, "start": {"3001"}})
	requireTag(t, rec.Body.String(), `data-action-error="invalid"`)
	assert.Equal(t, "1001", getChannel(t, c, "argon-tv-cnn").Spec.Number, "nor renumbered in bulk")

	// A dry run numbers nothing: the number is the person's still.
	dry, dc := lineupFixture(t, false, nil)
	body = get(t, dry, "/livetv/channels").Body.String()
	assert.NotRegexp(t, disabledAttr, tagWith(t, rowOf(t, body, "argon-tv-espn"), `name="number"`))
	rec = postTo(t, dry, "/livetv/channels/media/argon-tv-espn", "channel-argon-tv-espn", url.Values{"gen": {"1"}, "number": {"2005"}})
	require.Empty(t, rec.Header().Get("HX-Retarget"), rec.Body.String())
	assert.Equal(t, "2005", getChannel(t, dc, "argon-tv-espn").Spec.Number)
}

func TestTheDryRunBannerCountsTheCategories(t *testing.T) {
	plan := &livetvstore.LineupPlan{PlannedAt: tvNow, DryRun: true}
	plan.Channels = []lineup.Assignment{
		{Key: "n:cnn", Name: "CNN", Group: "USA: NEWS", Category: "News", Rule: "News", Number: "1001"},
		{Key: "n:espn", Name: "ESPN", Group: "USA: SPORTS", Category: "Sports", Rule: "Sports", Number: "2001"},
		{Key: "n:espnu", Name: "ESPNU", Group: "USA: SPORTS", Category: "Sports", Rule: "Sports", Number: "2002"},
	}
	plan.Categories = []lineup.Category{{Name: "News", Base: 1000, Channels: 1}, {Name: "Sports", Base: 2000, Channels: 2}}
	plan.Dropped = []lineup.Dropped{
		{Key: "n:espn2", Name: "ESPN", Reason: lineup.ReasonDuplicate, DuplicateOf: "n:espn"},
		{Key: "n:espnu2", Name: "ESPNU", Reason: lineup.ReasonDuplicate, DuplicateOf: "n:espnu"},
		{Key: "n:shop", Name: "Shop", Reason: lineup.ReasonOff},
		{Key: "n:hbo", Name: "HBO", Reason: lineup.ReasonUnmatched},
	}
	srv, _ := lineupFixture(t, false, plan)
	body := get(t, srv, "/livetv/channels").Body.String()
	banner := body[strings.Index(body, `data-lineup-dry-run="media/argon-tv"`):]
	banner = banner[:strings.Index(banner, `id="channels-filter"`)]
	assert.Contains(t, banner, "Dry run")
	assert.Contains(t, tagWith(t, banner, `data-dry-run-category="News"`), "data-slot=\"badge\"")
	assert.Contains(t, banner, "News 1")
	assert.Contains(t, banner, "Sports 2")
	for reason, want := range map[string]string{"duplicate": "2 duplicate", "off": "1 off", "unmatched": "1 unmatched"} {
		requireTag(t, banner, `data-dry-run-dropped="`+reason+`"`)
		assert.Contains(t, banner, want)
	}
	assert.NotContains(t, banner, `data-dry-run-dropped="excluded"`, "a reason nothing was dropped for is not named")

	espn := rowOf(t, body, "argon-tv-espn")
	assert.NotRegexp(t, disabledAttr, tagWith(t, espn, `name="number"`), "a dry run numbers nothing")
	// The category picker offers the plan's categories while the
	// provider's status has none.
	picker := get(t, srv, "/livetv/channels/media/argon-tv-espn/category").Body.String()
	requireTag(t, picker, `data-templ-value="Sports"`)
	requireTag(t, picker, `data-templ-value="News"`)

	// The playlist pane shows where the plan puts each entry, or why not.
	pane := get(t, srv, "/livetv/channels/playlist?provider=media%2Fargon-tv&all=1").Body.String()
	assert.Contains(t, entryOf(t, pane, "n:cnn"), `data-plan-category="News"`)
	assert.Contains(t, entryOf(t, pane, "n:espn"), `data-plan-category="Sports"`)
	assert.Contains(t, entryOf(t, pane, "n:espn2"), `data-plan-dropped="duplicate"`)
	assert.Contains(t, entryOf(t, pane, "n:hbo"), `data-plan-dropped="unmatched"`)

	// Before the controller stored a plan, the banner says so.
	none, _ := lineupFixture(t, false, nil)
	body = get(t, none, "/livetv/channels").Body.String()
	banner = body[strings.Index(body, `data-lineup-dry-run="media/argon-tv"`):]
	assert.Contains(t, banner[:strings.Index(banner, `id="channels-filter"`)], "not planned yet")
	// Enabled is no dry run.
	on, _ := lineupFixture(t, true, nil)
	assert.NotContains(t, get(t, on, "/livetv/channels").Body.String(), "data-lineup-dry-run")
}

// disabledAttr is a tag's disabled attribute, never a class naming it.
var disabledAttr = regexp.MustCompile(`\sdisabled(\s|>|=|/)`)

// entryOf is the playlist pane's entry key in body, through the next entry.
func entryOf(t *testing.T, body, key string) string {
	t.Helper()
	i := strings.Index(body, `data-playlist-entry="`+key+`"`)
	require.GreaterOrEqual(t, i, 0, key)
	end := strings.Index(body[i+1:], `data-playlist-entry="`)
	if end < 0 {
		return body[i:]
	}
	return body[i : i+1+end]
}
