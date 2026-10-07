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
	"compress/gzip"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

var update = flag.Bool("update", false, "rewrite golden files")

var from = time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)

func open(t *testing.T) []byte {
	b, err := os.ReadFile("../../../test/data/iptv/guide.xml")
	require.NoError(t, err)
	return b
}

func TestParseReadsChannelsAndTheWantedProgrammesInTheWindow(t *testing.T) {
	g, err := xmltv.Parse(bytes.NewReader(open(t)), map[string]bool{"bbc1.uk": true}, from, from.AddDate(0, 0, 7))
	require.NoError(t, err)
	assert.Equal(t, []xmltv.Channel{
		{ID: "bbc1.uk", DisplayNames: []string{"BBC One HD"}, Icon: "http://logo.example/bbc1.png"},
		{ID: "news.uk", DisplayNames: []string{"News Live", "News, Live"}},
	}, g.Channels)
	require.Len(t, g.Programmes["bbc1.uk"], 2, "only the window, only wanted channels")
	assert.Empty(t, g.Programmes["news.uk"])
	p := g.Programmes["bbc1.uk"][0]
	assert.Equal(t, time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC), p.Start.UTC())
	assert.Contains(t, string(p.Inner), `<desc lang="en">Headlines &amp; weather.</desc>`, "inner XML is kept verbatim, escapes included")
}

func TestParseReadsGzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(open(t))
	require.NoError(t, zw.Close())
	g, err := xmltv.Parse(&buf, nil, from, from.AddDate(0, 0, 7))
	require.NoError(t, err)
	assert.Len(t, g.Channels, 2)
	assert.Empty(t, g.Programmes, "a nil want parses channels only")
}

func TestParseCapsTheUncompressedSize(t *testing.T) {
	defer xmltv.SetMaxBytes(1 << 20)()
	r := io.MultiReader(strings.NewReader("<tv>"), &repeat{b: []byte("<!-- padding -->"), n: 1<<20/16 + 1})
	_, err := xmltv.Parse(r, nil, from, from.AddDate(0, 0, 7))
	require.ErrorIs(t, err, xmltv.ErrResponseTooLarge)
}

func TestParseRefusesWhatIsNotXMLTV(t *testing.T) {
	_, err := xmltv.Parse(strings.NewReader("#EXTM3U\n"), nil, from, from.AddDate(0, 0, 7))
	require.Error(t, err)
}

func TestBuildGuide(t *testing.T) {
	g, err := xmltv.Parse(bytes.NewReader(open(t)), map[string]bool{"bbc1.uk": true}, from, from.AddDate(0, 0, 7))
	require.NoError(t, err)
	lineup := []xmltv.LineupChannel{
		{Number: "1002", Name: "Dummy Chan", DummyMinutes: 60},
		{Number: "1001", Name: "BBC One", Logo: "http://logo.example/bbc1.png", Map: &xmltv.Mapping{Guide: "main", ChannelID: "bbc1.uk"}, TimeshiftMinutes: 60},
	}
	var out bytes.Buffer
	require.NoError(t, xmltv.BuildGuide(&out, lineup, map[string]xmltv.Guide{"main": g}, from, 7))
	s := out.String()
	assert.Contains(t, s, `<channel id="1001"><display-name>BBC One</display-name><icon src="http://logo.example/bbc1.png"></icon></channel>`)
	assert.Contains(t, s, `<programme start="20261007190000 +0000" stop="20261007200000 +0000" channel="1001">`, "timeshifted by an hour")
	assert.Less(t, strings.Index(s, `channel id="1001"`), strings.Index(s, `channel id="1002"`), "channels sorted by number")
	assert.Equal(t, 7*24, strings.Count(s, `channel="1002"`), "one dummy block per hour for seven days")
	var again bytes.Buffer
	require.NoError(t, xmltv.BuildGuide(&again, lineup, map[string]xmltv.Guide{"main": g}, from, 7))
	assert.Equal(t, s, again.String(), "the same input gives the same bytes")
	golden := "../../../test/data/iptv/guide_built.golden.xml"
	if *update {
		require.NoError(t, os.WriteFile(golden, out.Bytes(), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	assert.Equal(t, string(want), s)
	built, err := xmltv.Parse(bytes.NewReader(out.Bytes()), map[string]bool{"1001": true}, from, from.AddDate(0, 0, 7))
	require.NoError(t, err, "the built guide parses as XMLTV")
	assert.Len(t, built.Programmes["1001"], 2)
}

func TestNumbersSortNumerically(t *testing.T) {
	lineup := []xmltv.LineupChannel{
		{Number: "10", Name: "Ten", DummyMinutes: 180},
		{Number: "9.2", Name: "Nine two", DummyMinutes: 180},
		{Number: "9", Name: "Nine", DummyMinutes: 180},
	}
	var out bytes.Buffer
	require.NoError(t, xmltv.BuildGuide(&out, lineup, nil, from, 1))
	s := out.String()
	assert.Less(t, strings.Index(s, `channel id="9"`), strings.Index(s, `channel id="9.2"`))
	assert.Less(t, strings.Index(s, `channel id="9.2"`), strings.Index(s, `channel id="10"`))
}

func TestAutoMap(t *testing.T) {
	g, err := xmltv.Parse(bytes.NewReader(open(t)), nil, from, from.AddDate(0, 0, 7))
	require.NoError(t, err)
	guides := map[string]xmltv.Guide{"main": g}
	m, ok := xmltv.AutoMap("bbc1.uk", "anything", guides)
	assert.True(t, ok)
	assert.Equal(t, xmltv.Mapping{Guide: "main", ChannelID: "bbc1.uk"}, m, "tvg-id first")
	m, ok = xmltv.AutoMap("", "NEWS LIVE", guides)
	assert.True(t, ok)
	assert.Equal(t, xmltv.Mapping{Guide: "main", ChannelID: "news.uk"}, m, "then a normalised display name")
	_, ok = xmltv.AutoMap("", "Unknown Channel", guides)
	assert.False(t, ok)
}

type repeat struct {
	b []byte
	n int64
}

func (r *repeat) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, io.EOF
	}
	n := 0
	for r.n > 0 && len(p)-n >= len(r.b) {
		n += copy(p[n:], r.b)
		r.n--
	}
	if n == 0 {
		r.n--
		return copy(p, r.b), nil
	}
	return n, nil
}
