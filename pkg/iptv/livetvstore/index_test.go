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

package livetvstore_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

var from = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

// built is the XEPG guide BuildGuide writes for one mapped channel with one
// programme of inner, and one hourly dummy channel.
func built(t *testing.T, inner string) []byte {
	t.Helper()
	guides := map[string]xmltv.Guide{"xmltv": {Programmes: map[string][]xmltv.Programme{"US-ESPN": {
		{Start: from.Add(19 * time.Hour), Stop: from.Add(22 * time.Hour), Inner: []byte(inner)},
	}}}}
	lineup := []xmltv.LineupChannel{
		{Number: "1001", Name: "ESPN", Logo: "https://i.example/espn.png", Map: &xmltv.Mapping{Guide: "xmltv", ChannelID: "US-ESPN"}},
		{Number: "1002", Name: "Local", DummyMinutes: 60},
	}
	var b bytes.Buffer
	require.NoError(t, xmltv.BuildGuide(&b, lineup, guides, from, 7))
	return b.Bytes()
}

func TestIndexGuideReadsWhatBuildGuideWrote(t *testing.T) {
	inner := `<title>NFL Football</title><sub-title>Cowboys at Eagles</sub-title><desc>Week 9.</desc>` +
		`<category>Sports</category><episode-num system="xmltv_ns">0.1.</episode-num><icon src="https://img.example/p.jpg"/><live/><new/>`
	ix, err := livetvstore.IndexGuide(bytes.NewReader(built(t, inner)), from)
	require.NoError(t, err)
	assert.Equal(t, from, ix.From)
	assert.Equal(t, []livetvstore.IndexChannel{{Number: "1001", Name: "ESPN", Logo: "https://i.example/espn.png"}, {Number: "1002", Name: "Local"}}, ix.Channels)
	require.NotEmpty(t, ix.Programmes)
	assert.Equal(t, livetvstore.IndexProgramme{Channel: "1001", Start: from.Add(19 * time.Hour), Stop: from.Add(22 * time.Hour),
		Title: "NFL Football", SubTitle: "Cowboys at Eagles", Desc: "Week 9.", Episode: "S01E02", Image: "https://img.example/p.jpg",
		Categories: []string{"Sports"}, Live: true, New: true}, ix.Programmes[0])
	assert.Len(t, ix.Programmes, 1+7*24, "the dummy channel's hourly blocks follow")
	assert.Equal(t, "1002", ix.Programmes[1].Channel)
	assert.Equal(t, "Local", ix.Programmes[1].Title)
	assert.Equal(t, from, ix.Programmes[1].Start)
}

func TestIndexGuideLabelsEpisodes(t *testing.T) {
	for _, c := range []struct{ num, want string }{
		{`<episode-num system="xmltv_ns">0.1.</episode-num>`, "S01E02"},
		{`<episode-num system="xmltv_ns">2.9/10.</episode-num>`, "S03E10"},
		{`<episode-num system="xmltv_ns">.4.</episode-num>`, "E05"},
		{`<episode-num system="onscreen">S01E02</episode-num>`, "S01E02"},
		{`<episode-num system="dd_progid">EP012345670002</episode-num>`, ""},
		{`<episode-num system="dd_progid">EP012345670002</episode-num><episode-num system="xmltv_ns">4.0.</episode-num>`, "S05E01"},
	} {
		ix, err := livetvstore.IndexGuide(bytes.NewReader(built(t, "<title>Show</title>"+c.num)), from)
		require.NoError(t, err)
		assert.Equal(t, c.want, ix.Programmes[0].Episode, c.num)
	}
}

func TestIndexGuideCutsALongDescription(t *testing.T) {
	ix, err := livetvstore.IndexGuide(bytes.NewReader(built(t, "<title>Show</title><desc>"+strings.Repeat("é", 2000)+"</desc>")), from)
	require.NoError(t, err)
	d := []rune(ix.Programmes[0].Desc)
	assert.Len(t, d, 600)
	assert.Equal(t, '…', d[599])
}
