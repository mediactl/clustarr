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

package grabplan_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// An episode whose file is not the shape its series is kept in takes
// another release over a transcoded file (owner, 2026-11-17) -- a
// release's shape is known only once its file is probed, so each is a
// download. Import refuses the ones that were not it and blocklists them
// for the owner; once MaxAspectTries of the releases covering the episode
// carry such a block, the file is final again, and no fourth is fetched
// (One Piece: no release of another shape exists). A block of any other
// kind is not a try.
func TestAWrongShapedFileIsTriedForAFewReleasesAndThenKept(t *testing.T) {
	titlesOf := []string{
		"Breaking.Bad.S01E01.720p.HDTV.x264-ONE",
		"Breaking.Bad.S01E01.720p.HDTV.x264-TWO",
		"Breaking.Bad.S01E01.720p.HDTV.x264-THREE",
		"Breaking.Bad.S01E01.720p.HDTV.x264-FOUR",
	}
	view := func(wrongShape bool, blocked int, reason commonv1.DownloadFailureReason) grabplan.View {
		cands := seasonCandidates(t, 1, 1, titlesOf...)
		require.Len(t, cands, 4)
		n := 0
		for i := range cands {
			if n < blocked {
				cands[i].Release.Blocked = &schema.ReleaseBlock{Scope: "Series/media/breaking-bad/uid-bb", Reason: string(reason)}
				n++
			}
		}
		ep := episode(1, 1)
		ep.Current = &decision.Current{Quality: q(t, "HDTV-720p"), Transcoded: true, WrongAspect: wrongShape}
		return seriesView(t, []grabplan.Covered{ep}, cands...)
	}
	rejected := commonv1.DownloadFailureImportRejected

	require.Empty(t, grabplan.Decide(view(false, 0, rejected)).NewEntries, "a transcoded file of the wanted shape is final")
	require.Len(t, grabplan.Decide(view(true, 0, rejected)).NewEntries, 1, "a file of the wrong shape is not")
	require.Len(t, grabplan.Decide(view(true, grabplan.MaxAspectTries-1, rejected)).NewEntries, 1, "nor after two tries")

	kept := grabplan.Decide(view(true, grabplan.MaxAspectTries, rejected))
	require.Empty(t, kept.NewEntries, "after three releases that were not it, the file is kept: %+v", kept.Decisions)

	require.Len(t, grabplan.Decide(view(true, grabplan.MaxAspectTries, commonv1.DownloadFailureEncrypted)).NewEntries, 1,
		"a release blocked for another fault was not a try")
}
