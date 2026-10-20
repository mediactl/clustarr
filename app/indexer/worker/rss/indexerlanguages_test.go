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

package rss_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/indexer/worker/rss"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// TestAReleaseCarriesItsIndexersAudioLanguages: the two usenet indexers'
// recorded answers to a search for Akira (2026-10-20), trimmed, through the
// real parser and the projection. nzbgeek sends a language attribute per
// audio track, nzb-life a list mixing codes and names; both report
// hallowed's untagged Blu-ray as Japanese and English, nzbgeek PD's DUAL as
// Japanese and Portuguese, and nzb-life the "MULTi.Jpn.Eng" whose "Eng" was
// its subtitles as Japanese alone.
func TestAReleaseCarriesItsIndexersAudioLanguages(t *testing.T) {
	langs := func(t *testing.T, fixture string) map[string][]string {
		t.Helper()
		f, err := os.Open(filepath.Join("..", "..", "..", "..", "test", "data", "torznab", fixture))
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		rels, err := torznab.ParseResults(f)
		require.NoError(t, err)
		out := map[string][]string{}
		for _, r := range rels {
			out[r.Title] = rss.ProjectRelease(r, "x", "usenet").Info.IndexerLanguages
		}
		return out
	}
	geek := langs(t, "nzbgeek_language_attrs.xml")
	require.Equal(t, []string{"English", "Japanese"}, geek["Akira.1988.BluRay.1080p.DDP.Atmos.5.1.x264-hallowed"])
	require.Equal(t, []string{"Japanese", "Portuguese"}, geek["Akira.1988.1080p.NF.WEB-DL.AAC2.0.x264.DUAL-PD"])
	require.Equal(t, []string{"English", "Japanese", "Spanish"}, geek["Akira.1988.1080p.BluRay.ENG.LATINO.JAP.DD5.1.H264-BEN.THE.MEN"])
	require.Empty(t, geek["Akira.1988.1080p.BluRay.x264-WiKi-AsRequested"], "an indexer that reports nothing leaves the title's reading")

	life := langs(t, "nzblife_language_attrs.xml")
	require.Equal(t, []string{"Japanese", "English"}, life["Akira.1988.BluRay.1080p.DDP.Atmos.5.1.x264-hallowed"])
	require.Equal(t, []string{"Japanese"}, life["Akira.1988.2160p.BDRip.TrueHD.7.1.Atmos.MULTi.Jpn.Eng.DoVi.HDR10.x265.10bit-MarkII"])
	require.Equal(t, []string{"Japanese", "English", "Italian"}, life["Akira.1988.4K.HDR.DV.2160p.BDRemux.Ita.Jap.Eng.x265-NAHOM"])
}
