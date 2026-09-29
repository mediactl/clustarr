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

package naming_test

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/naming"
)

func TestRenderCleanTitleStripsApostrophesKeepsBang(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Movie CleanTitle}", naming.Context{Title: "The Series Name's Title!"})
	require.NoError(t, err)
	require.Equal(t, "The Series Names Title!", got)
}

func TestRenderTitleTheMovesLeadingThe(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Movie TitleThe}", naming.Context{Title: "The Series Name"})
	require.NoError(t, err)
	require.Equal(t, "Series Name, The", got)
}

// TestRenderColonReplacementModes renders {Movie Title}: {Movie CleanTitle}
// drops a colon a space follows before any mode applies, as Radarr's does.
func TestRenderColonReplacementModes(t *testing.T) {
	tests := []struct {
		name string
		mode naming.ColonReplacement
		want string
	}{
		{"delete", naming.ColonDelete, "Spider-Man Into the Spider-Verse"},
		{"dash", naming.ColonDash, "Spider-Man- Into the Spider-Verse"},
		{"spaceDash", naming.ColonSpaceDash, "Spider-Man - Into the Spider-Verse"},
		{"spaceDashSpace", naming.ColonSpaceDashSpace, "Spider-Man - Into the Spider-Verse"},
		{"smart", naming.ColonSmart, "Spider-Man - Into the Spider-Verse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := naming.NewEngine(naming.Config{ColonReplacement: tt.mode})
			got, err := e.Render("{Movie Title}", naming.Context{Title: "Spider-Man: Into the Spider-Verse"})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestCleanTitleIsRadarrs: {Movie CleanTitle} is Radarr's
// FileNameBuilder.CleanTitle -- "&" becomes "and", "/" a space, an
// apostrophe, colon, question mark or comma goes before a space, the end or
// a contraction's ending, a lone mark between spaces goes, brackets go,
// diacritics go, and a run of one separator collapses -- held to every
// title Radarr wrote into the owner's movie library (2026-09-29). Under
// ColonReplacement smart, a colon inside a word is the dash Radarr wrote
// ("GANTZ:O" -> "GANTZ-O").
func TestCleanTitleIsRadarrs(t *testing.T) {
	f, err := os.Open("../../test/data/naming/radarr-clean-titles.tsv")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	e := naming.NewEngine(naming.Config{ColonReplacement: naming.ColonSmart})
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		title, want, ok := strings.Cut(line, "\t")
		require.True(t, ok, line)
		got, err := e.Render("{Movie CleanTitle}", naming.Context{Title: title})
		require.NoError(t, err)
		if got != want {
			t.Errorf("%q: got %q, Radarr wrote %q", title, got, want)
		}
		n++
	}
	require.NoError(t, sc.Err())
	require.Greater(t, n, 800)
}
