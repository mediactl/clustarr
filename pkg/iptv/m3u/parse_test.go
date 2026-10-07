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

package m3u_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/m3u"
)

func fixture(t testing.TB) []byte {
	b, err := os.ReadFile("../../../test/data/iptv/m3u_plus.m3u")
	require.NoError(t, err)
	return b
}

func TestParseReadsEveryAttributeForm(t *testing.T) {
	pl, err := m3u.Parse(bytes.NewReader(fixture(t)))
	require.NoError(t, err)
	assert.Equal(t, []string{"http://epg.example/guide.xml.gz", "http://epg.example/alt.xml"}, pl.GuideURLs)
	require.Len(t, pl.Entries, 6)
	assert.Equal(t, m3u.Entry{
		Key: "t:bbc1.uk", Name: "BBC One HD", Group: "UK | Entertainment", TvgID: "bbc1.uk",
		TvgName: "BBC One HD", Logo: "http://logo.example/bbc1.png", URL: "http://provider.example/live/user/pass/1001.ts",
	}, pl.Entries[0])
	assert.Equal(t, "News, Live", pl.Entries[1].Name, "a comma inside the name survives")
	assert.Equal(t, "News, Live", pl.Entries[1].TvgName, "a comma inside quotes survives")
	assert.Equal(t, "VLC/3.0.20", pl.Entries[1].UserAgent)
	assert.Equal(t, "Sports", pl.Entries[2].Group, "an unquoted attribute is read")
	assert.Equal(t, "dup.id", pl.Entries[2].TvgID)
	assert.Equal(t, "Sports 1", pl.Entries[2].Name)
	assert.Equal(t, "Misc", pl.Entries[4].Group, "#EXTGRP gives the group when group-title is absent")
	assert.Equal(t, "No Attributes", pl.Entries[4].Name)
	assert.Equal(t, "t:nogroup", pl.Entries[5].Key, "a blank line between entries is skipped")
}

func TestKeysAreTvgIDWhenUniqueElseNameBased(t *testing.T) {
	pl, err := m3u.Parse(bytes.NewReader(fixture(t)))
	require.NoError(t, err)
	assert.Equal(t, m3u.NameKey("UK | News", "News, Live"), pl.Entries[1].Key, "an empty tvg-id is name-based")
	assert.Equal(t, m3u.NameKey("Sports", "Sports 1"), pl.Entries[2].Key, "a duplicated tvg-id is name-based")
	assert.Equal(t, m3u.NameKey("Sports", "Sports 2"), pl.Entries[3].Key)
	assert.NotEqual(t, pl.Entries[2].Key, pl.Entries[3].Key)
	assert.Regexp(t, `^n:[a-z2-7]{26}$`, pl.Entries[2].Key)
	for _, e := range pl.Entries {
		assert.LessOrEqual(t, len(e.Key), 64)
	}
}

func TestAKeyIsStableAcrossStreamURLTokenChanges(t *testing.T) {
	a, err := m3u.Parse(bytes.NewReader(fixture(t)))
	require.NoError(t, err)
	b, err := m3u.Parse(bytes.NewReader(bytes.ReplaceAll(fixture(t), []byte("/user/pass/"), []byte("/user/rotated/"))))
	require.NoError(t, err)
	for i := range a.Entries {
		assert.Equal(t, a.Entries[i].Key, b.Entries[i].Key)
	}
}

func TestABOMAndCRLFParseTheSame(t *testing.T) {
	want, err := m3u.Parse(bytes.NewReader(fixture(t)))
	require.NoError(t, err)
	crlf := append([]byte("\uFEFF"), bytes.ReplaceAll(fixture(t), []byte("\n"), []byte("\r\n"))...)
	got, err := m3u.Parse(bytes.NewReader(crlf))
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestALongTvgIDFallsBackToANameKey(t *testing.T) {
	long := strings.Repeat("x", 63)
	pl, err := m3u.Parse(strings.NewReader("#EXTM3U\n#EXTINF:-1 tvg-id=\"" + long + "\",Long\nhttp://p.example/1.ts\n"))
	require.NoError(t, err)
	assert.Equal(t, m3u.NameKey("", "Long"), pl.Entries[0].Key)
}

func playlistOf(n int) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=\"c%d\" group-title=\"G%d\",Channel %d\nhttp://p.example/live/u/p/%d.ts\n", i, i%50, i, i)
	}
	return b.String()
}

func TestTheCapsAreErrors(t *testing.T) {
	_, err := m3u.Parse(io.MultiReader(strings.NewReader("#EXTM3U\n"), io.LimitReader(neverEnding{}, m3u.MaxBytes+2)))
	require.ErrorIs(t, err, m3u.ErrResponseTooLarge)
	_, err = m3u.Parse(strings.NewReader(playlistOf(m3u.MaxEntries + 1)))
	require.ErrorIs(t, err, m3u.ErrTooManyEntries)
}

func TestAMissingHeaderIsAnError(t *testing.T) {
	_, err := m3u.Parse(strings.NewReader("<html>login</html>"))
	require.ErrorContains(t, err, "not an M3U playlist")
	_, err = m3u.Parse(strings.NewReader(""))
	require.ErrorContains(t, err, "not an M3U playlist")
}

func BenchmarkParseFiftyThousand(b *testing.B) {
	pl := playlistOf(50000)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := m3u.Parse(strings.NewReader(pl)); err != nil {
			b.Fatal(err)
		}
	}
}

// neverEnding is comment lines forever, so the cap, not a parse error, ends it.
type neverEnding struct{}

func (neverEnding) Read(p []byte) (int, error) {
	for i := range p {
		if i%64 == 63 {
			p[i] = '\n'
		} else {
			p[i] = '#'
		}
	}
	return len(p), nil
}
