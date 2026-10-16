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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/import/importplan"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// setGrab is the first part's import of a set of Heat's parts, one payload
// per part, each holding the file named in files (part order) probed as
// probes says: part 1's entry, the others as the downloads stage passes
// them.
func setGrab(t *testing.T, wd *world, files []string, probes []*commonv1.MediaInfo) *grab {
	t.Helper()
	of := int32(len(files))
	var parts []importplan.PartInput
	var root1 string
	for i, name := range files {
		wd.probes[name] = probes[i]
		root := wd.payload(map[string]int64{name: big})
		n := int32(i + 1)
		title := fmt.Sprintf("Heat.1995.Part%d.1080p.BluRay.x264-GRP", n)
		if n == 1 {
			root1 = root
			continue
		}
		parts = append(parts, importplan.PartInput{
			Entry: catalogv1alpha1.DownloadEntry{
				ID: fmt.Sprintf("dl-heat-part%d", n), UID: fmt.Sprintf("entry-heat-part%d", n),
				Release: catalogv1alpha1.DownloadRelease{Title: title, Protocol: commonv1.ProtocolUsenet},
				Phase:   commonv1.DownloadPhaseCompleted,
				Set:     &catalogv1alpha1.EntrySet{ID: "parts:heat", Part: n, Of: of},
			},
			Transfer: &schema.TransferRecord{ContentRoot: root},
		})
	}
	g := wd.grab(commonv1.MediaKindMovie, "heat", "Heat.1995.Part1.1080p.BluRay.x264-GRP", root1)
	g.in.Entry.Set = &catalogv1alpha1.EntrySet{ID: "parts:heat", Part: 1, Of: of}
	g.in.Parts = parts
	g.in.RuntimeMinutes = 170
	g.in.Identity = decision.Identity{Titles: []string{"Heat"}, Year: 1995}
	return g
}

func hd(minutes int64) *commonv1.MediaInfo {
	return &commonv1.MediaInfo{VideoCodec: "h264", Width: 1920, Height: 1080, RuntimeMillis: minutes * 60 * 1000}
}

// A movie posted in parts, each part its own download (a set's grab): the
// first part's import walks every part's payload and joins them in part
// order, though the payloads' file names, obfuscated, carry no part.
func TestASetsPartsInTheirOwnDownloadsImportAsOneJoinedFile(t *testing.T) {
	wd := newWorld(t)
	joined := fakeJoin(t, wd)
	g := setGrab(t, wd, []string{"x7Gq2.mkv", "a91Bz.mkv"}, []*commonv1.MediaInfo{hd(85), hd(85)})
	g.inspected()
	moves := g.plan().Plan.Moves
	require.Len(t, moves, 1, "one movie, one file")
	require.Len(t, moves[0].Join, 2)
	assert.Equal(t, []string{"x7Gq2.mkv", "a91Bz.mkv"}, []string{filepath.Base(moves[0].Join[0]), filepath.Base(moves[0].Join[1])},
		"joined in part order, read from the downloads, not the names")

	dec := g.executed()
	assert.Equal(t, lifecycle.ImportImported, dec.Verdict, "%+v", dec.Summary)
	assert.Equal(t, []string{"x7Gq2.mkv", "a91Bz.mkv"}, *joined)
	st, err := os.Stat(moves[0].Dest)
	require.NoError(t, err)
	assert.Equal(t, int64(2*big), st.Size())
}

// A set imports as the whole film or not at all: parts of different
// encodes join to nothing, and parts that join short of the film are
// missing one; both are the release's fault, never one part imported.
func TestASetThatIsNotTheWholeFilmIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		probes []*commonv1.MediaInfo
		says   string
	}{
		"parts of other encodes": {
			[]*commonv1.MediaInfo{hd(85), {VideoCodec: "hevc", Width: 1920, Height: 1080, RuntimeMillis: 85 * 60 * 1000}},
			"did not join",
		},
		"a part short": {[]*commonv1.MediaInfo{hd(60), hd(60)}, "a part is missing"},
	} {
		t.Run(name, func(t *testing.T) {
			wd := newWorld(t)
			fakeJoin(t, wd)
			g := setGrab(t, wd, []string{"p1.mkv", "p2.mkv"}, tc.probes)
			dec := g.inspected()
			assert.Nil(t, dec.Dispatch, "nothing is executed: %+v", dec.Summary)
			assert.Equal(t, commonv1.ImportClassReleaseFault, dec.Summary.Class, "%+v", dec.Summary)
			assert.Contains(t, dec.Summary.Message, tc.says)
		})
	}
}
