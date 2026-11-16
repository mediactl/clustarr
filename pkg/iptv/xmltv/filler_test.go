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

package xmltv_test

import (
	"bytes"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

func TestFillerNamesASourcesPlaceholders(t *testing.T) {
	for _, title := range []string{
		"Paid Programming", "SIGN OFF", "Sign-Off", "Off-Air", "No Data", "Channel No Longer Available",
		"No EVENT Today", "No Games Scheduled", "To Be Announced", "TBA", "Marlins Baseball Sign Off",
		"Cricket Filler", "Upcoming - MLB Network Strike Zone", "Blackout", "Movie",
	} {
		assert.True(t, xmltv.Filler([]byte("<title lang=\"en\">"+title+"</title>")), title)
	}
	for _, inner := range []string{
		"<title>SportsCenter</title>", "<title>Sign Off Stories</title>", "<title>News</title>",
		"<title>No Contest: Off Tour</title>", "<title>Upcoming</title>", "<title>Test Cricket</title>",
		"<title>Dark Side of the Ring</title>", "<title>Local Programming</title>",
		// A real title that is also a placeholder's word says something else.
		"<title>Blackout</title><desc>A power cut strands four strangers.</desc>",
		"<title>Movie</title><sub-title>The Making Of</sub-title>",
		"<title>Unclosed",
	} {
		assert.False(t, xmltv.Filler([]byte(inner)), inner)
	}
	// A title repeated as the episode's says nothing else.
	assert.True(t, xmltv.Filler([]byte("<title>Blackout</title><sub-title>Blackout</sub-title>")))
	assert.True(t, xmltv.Filler([]byte("<title>Paid Programming 1 Inspiration Ministries</title><sub-title>Paid Programming 1 Inspiration Ministries</sub-title>")))
	assert.False(t, xmltv.Filler([]byte("<title>Portlandia</title><sub-title>Blackout</sub-title>")))
	// A placeholder is one whatever its description says.
	assert.True(t, xmltv.Filler([]byte("<title>Paid Programming</title><desc>Sponsored television programming.</desc>")))
}

// The built guide lists a channel's programmes without its source's
// placeholders, and a channel that had only those lists none.
func TestBuildGuideLeavesFillerOut(t *testing.T) {
	from := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	prog := func(ch string, hour int, title string) xmltv.Programme {
		return xmltv.Programme{Channel: ch, Start: from.Add(time.Duration(hour) * time.Hour), Stop: from.Add(time.Duration(hour+1) * time.Hour), Inner: []byte("<title>" + title + "</title>")}
	}
	guides := map[string]xmltv.Guide{"g": {Programmes: map[string][]xmltv.Programme{
		"a": {prog("a", 0, "SIGN OFF"), prog("a", 1, "College Football"), prog("a", 2, "Paid Programming")},
		"b": {prog("b", 0, "Channel No Longer Available")},
	}}}
	lineup := []xmltv.LineupChannel{
		{Number: "1", Name: "A", Map: &xmltv.Mapping{Guide: "g", ChannelID: "a"}},
		{Number: "2", Name: "B", Map: &xmltv.Mapping{Guide: "g", ChannelID: "b"}},
	}
	for _, e := range []*xmltv.Enricher{nil, {}} {
		var out bytes.Buffer
		require.NoError(t, xmltv.BuildGuideWith(&out, lineup, guides, from, 1, e))
		s := out.String()
		assert.Equal(t, 1, bytes.Count(out.Bytes(), []byte("<programme ")), s)
		assert.Contains(t, s, "College Football")
		assert.Contains(t, s, `<channel id="2">`, "the channel stays, with no listing")
	}
}

// TestFillerOverAGuide prints what Filler takes out of the built guide at
// CLUSTARR_FILLER_GUIDE, a tuner's xmltv.xml; skipped without it.
func TestFillerOverAGuide(t *testing.T) {
	path := os.Getenv("CLUSTARR_FILLER_GUIDE")
	if path == "" {
		t.Skip("CLUSTARR_FILLER_GUIDE names no guide")
	}
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	every := func(cs []xmltv.Channel) map[string]bool {
		want := map[string]bool{}
		for _, c := range cs {
			want[c.ID] = true
		}
		return want
	}
	g, err := xmltv.ParseFunc(f, every, time.Time{}, time.Now().AddDate(1, 0, 0))
	require.NoError(t, err)
	now := time.Now()
	titles := map[string]int{}
	var all, out, onNow, onNowOut, emptied int
	for _, ps := range g.Programmes {
		kept := 0
		for _, p := range ps {
			all++
			on := !p.Start.After(now) && p.Stop.After(now)
			if on {
				onNow++
			}
			if !xmltv.Filler(p.Inner) {
				kept++
				continue
			}
			out++
			if on {
				onNowOut++
			}
			i := max(bytes.Index(p.Inner, []byte("</title>")), 0)
			j := bytes.LastIndexByte(p.Inner[:i], '>')
			titles[string(p.Inner[j+1:i])]++
		}
		if kept == 0 && len(ps) > 0 {
			emptied++
		}
	}
	t.Logf("%d of %d programmes are filler; %d of %d on now; %d of %d channels left with none", out, all, onNowOut, onNow, emptied, len(g.Programmes))
	names := make([]string, 0, len(titles))
	for n := range titles {
		names = append(names, n)
	}
	sort.Slice(names, func(a, b int) bool { return titles[names[a]] > titles[names[b]] })
	for _, n := range names {
		t.Logf("%6d  %s", titles[n], n)
	}
}
