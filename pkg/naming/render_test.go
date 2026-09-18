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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

func TestRenderSubstitutesSimpleTokens(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{Kind: commonv1.MediaKindMovie, Title: "The Matrix", Year: 1999}

	got, err := e.Render("{Movie Title} ({Release Year})", c)

	require.NoError(t, err)
	require.Equal(t, "The Matrix (1999)", got)
}

func TestRenderLeavesLiteralTextAlone(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("no tokens here", naming.Context{})
	require.NoError(t, err)
	require.Equal(t, "no tokens here", got)
}

func TestRenderOmitsWrapperWhenTokenIsEmpty(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Movie Title}{-Release Group}", naming.Context{Title: "Heat"})
	require.NoError(t, err)
	require.Equal(t, "Heat", got, "empty Release Group must drop the leading dash too")
}

func TestRenderKeepsWrapperWhenTokenIsPresent(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{Movie Title}{-Release Group}", naming.Context{Title: "Heat", ReleaseGroup: "RlsGrp"})
	require.NoError(t, err)
	require.Equal(t, "Heat-RlsGrp", got)
}

func TestRenderProviderIDTokens(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{Title: "The Matrix", Year: 1999, TmdbID: "603"}
	got, err := e.Render("{Movie Title} ({Release Year}) [tmdbid-{TmdbId}]", c)
	require.NoError(t, err)
	require.Equal(t, "The Matrix (1999) [tmdbid-603]", got)
}

func TestRenderSplitBracketAcrossTwoAdjacentTokens(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{MediaInfo: commonv1.MediaInfo{Audio: []commonv1.AudioStream{{Codec: "EAC3", Channels: 6}}}}
	got, err := e.Render("{[MediaInfo AudioCodec}{ MediaInfo AudioChannels]}", c)
	require.NoError(t, err)
	require.Equal(t, "[EAC3 5.1]", got, "note A2's split-bracket idiom: two single-brace tokens forming one pair")
}

func TestRenderZeroPadsSeasonAndEpisode(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{Season: 1, Episodes: []int{2}}
	got, err := e.Render("S{season:00}E{episode:00}", c)
	require.NoError(t, err)
	require.Equal(t, "S01E02", got)
}

func TestRenderZeroPadsAbsoluteToThreeDigits(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render("{absolute:000}", naming.Context{Absolute: []int{7}})
	require.NoError(t, err)
	require.Equal(t, "007", got)
}

func TestRenderTruncatesEpisodeCleanTitleToNChars(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	long := strings.Repeat("Long Episode Title ", 10) // 190 runes
	got, err := e.Render("{Episode CleanTitle:90}", naming.Context{EpisodeTitle: long})
	require.NoError(t, err)
	require.Len(t, got, 90)
	require.Equal(t, long[:90], got)
}

// TestRenderMalformedInput feeds Render garbage, truncated and empty
// templates, per the global constraint that every parser has a test for
// each. Every case must return a well-defined (output, error) pair and
// must not panic; none of these are valid *arr templates, but Render's
// single-pass, non-nesting regex (\{[^{}]*\}) gives every one of them a
// well-defined, if sometimes surprising, outcome rather than failing to
// compile or looping.
func TestRenderMalformedInput(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string
		ctx     naming.Context
		want    string
		wantErr error // nil means no error
	}{
		{
			name: "empty template",
			tmpl: "",
			ctx:  naming.Context{},
			want: "",
		},
		{
			name: "unbalanced open brace: no closing brace, so no token matches and the text is literal",
			tmpl: "{Movie Title",
			ctx:  naming.Context{Title: "Heat"},
			want: "{Movie Title",
		},
		{
			name: "unbalanced close brace: no opening brace, so no token matches and the text is literal",
			tmpl: "Movie Title}",
			ctx:  naming.Context{Title: "Heat"},
			want: "Movie Title}",
		},
		{
			name:    "empty token: {} has no token name",
			tmpl:    "{}",
			ctx:     naming.Context{},
			want:    "",
			wantErr: naming.ErrUnknownToken,
		},
		{
			name: "nested braces: the grammar does not nest, so the outer braces are literal text around one inner token",
			tmpl: "{{Movie Title}}",
			ctx:  naming.Context{Title: "Heat"},
			want: "{Heat}",
		},
		{
			name:    "wrapper character only, no token name",
			tmpl:    "{-}",
			ctx:     naming.Context{},
			want:    "",
			wantErr: naming.ErrUnknownToken,
		},
		{
			name:    "modifier only, no token name",
			tmpl:    "{:00}",
			ctx:     naming.Context{},
			want:    "",
			wantErr: naming.ErrUnknownToken,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := naming.NewEngine(naming.Config{})

			var got string
			var err error
			require.NotPanics(t, func() {
				got, err = e.Render(tt.tmpl, tt.ctx)
			})

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.want, got)
		})
	}
}
