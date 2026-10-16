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

package importplan_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
)

const (
	heatPart1 = "Heat.1995.Part1.1080p.BluRay.x264-GRP.mkv"
	heatPart2 = "Heat.1995.Part2.1080p.BluRay.x264-GRP.mkv"
)

// fakeJoin has the world's import agent join parts by their sizes: the
// world's files are sparse, not media. It records what it joined.
func fakeJoin(t *testing.T, wd *world) *[]string {
	t.Helper()
	joined := &[]string{}
	wd.w.Join = func(_ context.Context, parts []string, out string) error {
		var size int64
		for _, p := range parts {
			st, err := os.Stat(p)
			if err != nil {
				return err
			}
			size += st.Size()
			*joined = append(*joined, filepath.Base(p))
		}
		f, err := os.Create(out) //nolint:gosec // under t.TempDir()
		if err != nil {
			return err
		}
		if err := f.Truncate(size); err != nil {
			return err
		}
		return f.Close()
	}
	return joined
}

// A movie released as Part 1 and Part 2 of one encode -- 28 Up's, 42 Up's
// and 49 Up's Blu-rays (2026-10-16), each imported as half the film --
// imports as one file, its parts joined in order; the walk meeting Part 2
// first changes nothing.
func TestAMovieReleasedInPartsImportsAsOneJoinedFile(t *testing.T) {
	wd := newWorld(t)
	for _, n := range []string{heatPart1, heatPart2} {
		wd.probes[n] = &commonv1.MediaInfo{VideoCodec: "h264", Width: 1920, Height: 1080, RuntimeMillis: 85 * 60 * 1000}
	}
	joined := fakeJoin(t, wd)
	root := wd.payload(map[string]int64{"b/" + heatPart1: big, "a/" + heatPart2: big})
	g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", root)
	g.inspected()
	moves := g.plan().Plan.Moves
	require.Len(t, moves, 1, "one movie, one file")
	require.Len(t, moves[0].Join, 2)
	assert.Equal(t, []string{heatPart1, heatPart2}, []string{filepath.Base(moves[0].Join[0]), filepath.Base(moves[0].Join[1])})

	dec := g.executed()
	assert.Equal(t, lifecycle.ImportImported, dec.Verdict, "%+v", dec.Summary)
	assert.Equal(t, []string{heatPart1, heatPart2}, *joined)
	st, err := os.Stat(moves[0].Dest)
	require.NoError(t, err)
	assert.Equal(t, int64(2*big), st.Size(), "the joined file is placed")
}

// What is not a movie's parts imports as it was: one file whose title says
// Part 1, parts whose numbers skip, parts of different encodes.
func TestWhatIsNotAMoviesPartsImportsAsItWas(t *testing.T) {
	for name, tc := range map[string]struct {
		files  map[string]*commonv1.MediaInfo
		joined bool
	}{
		"a lone file titled Part 1": {files: map[string]*commonv1.MediaInfo{
			heatPart1: {VideoCodec: "h264", Width: 1920, Height: 1080},
		}},
		"parts 1 and 3": {files: map[string]*commonv1.MediaInfo{
			heatPart1: {VideoCodec: "h264", Width: 1920, Height: 1080},
			"Heat.1995.Part3.1080p.BluRay.x264-GRP.mkv": {VideoCodec: "h264", Width: 1920, Height: 1080},
		}},
		"parts of different encodes": {files: map[string]*commonv1.MediaInfo{
			heatPart1: {VideoCodec: "h264", Width: 1920, Height: 1080},
			heatPart2: {VideoCodec: "hevc", Width: 1920, Height: 1080},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			wd := newWorld(t)
			sizes := map[string]int64{}
			for n, mi := range tc.files {
				wd.probes[n] = mi
				sizes[n] = big
			}
			fakeJoin(t, wd)
			g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.1080p.BluRay.x264-GRP", wd.payload(sizes))
			g.inspected()
			moves := g.plan().Plan.Moves
			require.Len(t, moves, 1)
			assert.Empty(t, moves[0].Join, "imported as one file, unjoined")
		})
	}
}

func TestPartNumbersReadFromTheFileName(t *testing.T) {
	for name, want := range map[string]int{
		"28.Up.1984.Part1.1080p.BluRay.x264-USURY.mkv":   1,
		"28.Up.1984.Part.2.1080p.BluRay.x264-USURY.mkv":  2,
		"Movie (1999) - pt3.mkv":                         3,
		"Movie.1999.CD1.DVDRip.XviD.avi":                 1,
		"Movie 1999 Disc 2.mkv":                          2,
		"No.Direction.Home.2005.Part.1.1080p.BluRay.mkv": 1,
		"The.Departed.2006.1080p.BluRay.mkv":             0,
		"The.Apartment.1960.1080p.BluRay.mkv":            0,
		"Smart.People.2008.1080p.mkv":                    0,
		"Partisan.2015.1080p.mkv":                        0,
	} {
		n, ok := importplan.PartNumber(name)
		if want == 0 {
			assert.Falsef(t, ok, "%s", name)
			continue
		}
		assert.Truef(t, ok, "%s", name)
		assert.Equalf(t, want, n, "%s", name)
	}
}
