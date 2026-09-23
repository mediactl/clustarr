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

package subarchive_test

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles/providers/internal/subarchive"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/internal/subarchive/subarchivetest"
)

func TestOpenReadsAZip(t *testing.T) {
	raw := subarchivetest.Zip(t,
		"readme.txt", "not a subtitle",
		"Movie.2010.srt", "1\n00:00:01,000 --> 00:00:02,000\nHi.\n",
	)
	a, err := subarchive.Open(raw, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, []string{"Movie.2010.srt"}, a.Names())
	b, err := a.Read("Movie.2010.srt")
	require.NoError(t, err)
	assert.Contains(t, string(b), "Hi.")
}

// SubDL serves some RAR archives under a .zip name (Bazarr's _open_archive),
// so a RAR is recognised by its bytes.
func TestOpenReadsARar(t *testing.T) {
	raw := subarchivetest.Rar(t,
		"notes.nfo", "x",
		"Show.S01E05.srt", "episode five",
		"Show.S01E06.srt", "episode six",
	)
	a, err := subarchive.Open(raw, 1<<20)
	require.NoError(t, err)
	assert.Equal(t, []string{"Show.S01E05.srt", "Show.S01E06.srt"}, a.Names())
	b, err := a.Read("Show.S01E06.srt")
	require.NoError(t, err)
	assert.Equal(t, "episode six", string(b))
}

func TestOpenRejectsABareSubtitle(t *testing.T) {
	_, err := subarchive.Open([]byte("1\n00:00:01,000 --> 00:00:02,000\nHi.\n"), 1<<20)
	require.ErrorIs(t, err, subarchive.ErrNotArchive)
}

// A member past the cap is refused, never read whole: the archive's own
// size field is not trusted, the bytes are counted.
func TestAnOversizedMemberIsRefused(t *testing.T) {
	big := strings.Repeat("a", 2048)
	a, err := subarchive.Open(subarchivetest.Zip(t, "big.srt", big), 1024)
	require.NoError(t, err)
	_, err = a.Read("big.srt")
	require.ErrorIs(t, err, subarchive.ErrTooLarge)

	_, err = subarchive.Open(subarchivetest.Rar(t, "big.srt", big), 1024)
	require.ErrorIs(t, err, subarchive.ErrTooLarge)
}

// SubDL's _first_subtitle_in_archive: directory entries and macOS resource
// forks win the extension test and yield a few KB of binary, so they are
// never candidates.
func TestJunkIsNeverASubtitle(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range []string{"subs/", "__MACOSX/subs/._Movie.srt", "subs/._Movie.srt", "subs/Movie.SRT"} {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, _ = w.Write([]byte("x"))
	}
	require.NoError(t, zw.Close())

	a, err := subarchive.Open(buf.Bytes(), 1<<20)
	require.NoError(t, err)
	assert.Equal(t, []string{"subs/Movie.SRT"}, a.Names())
}

func TestPick(t *testing.T) {
	pack := []string{"Show.S01E04.srt", "Show.S01E05.forced.srt", "Show.S01E05.srt", "Show.S02E05.srt"}
	for _, tc := range []struct {
		name            string
		names           []string
		season, episode int
		forced          bool
		want            string
		err             error
	}{
		{"nothing", nil, 1, 5, false, "", subarchive.ErrNoSubtitle},
		{"a single member is taken whatever it is named", []string{"whatever.srt"}, 1, 5, false, "whatever.srt", nil},
		{"the episode the name spells", pack, 1, 5, false, "Show.S01E05.srt", nil},
		{"forced is skipped unless wanted", pack, 1, 5, true, "Show.S01E05.forced.srt", nil},
		{"the season must agree when both give one", pack, 2, 5, false, "Show.S02E05.srt", nil},
		{"no name confirms the episode: nothing, not a guess", pack, 1, 9, false, "", subarchive.ErrNoSubtitle},
		{"a movie takes the first non-forced", []string{"Movie.forced.srt", "Movie.srt"}, 0, 0, false, "Movie.srt", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := subarchive.Pick(tc.names, tc.season, tc.episode, tc.forced)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
