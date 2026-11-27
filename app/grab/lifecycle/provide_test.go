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

package lifecycle_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// A pack accounts for the files it holds: where the import's decision names
// the episodes its files are, the entry names those from then on, and the
// episodes it was grabbed for and does not hold are the plan's to have
// searched again. A decision that names none leaves the entry as it was.
func TestAnEntryNamesTheEpisodesItsImportSaysItHolds(t *testing.T) {
	grabbed := []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 14}, {Season: 1, Number: 15}}
	holds := []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 1}, {Season: 1, Number: 2}}
	freed := []schema.ItemRef{
		{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "show-s01e14", UID: "u14"}},
		{Kind: "Episode", Ref: schema.Ref{Namespace: "media", Name: "show-s01e15", UID: "u15"}},
	}
	held := lifecycle.ImportDecision{
		Summary: catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseHeld, Class: commonv1.ImportClassItemState},
		Verdict: lifecycle.ImportHeld,
	}

	e := usenetEntry(commonv1.DownloadPhaseCompleted)
	e.Episodes = grabbed
	v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, e.Dispatch.Seq, commonv1.DownloadStageDone))
	v.Import = map[types.UID]lifecycle.ImportDecision{types.UID(e.UID): held}
	p := lifecycle.Decide(v)
	assert.Equal(t, grabbed, only(p).Episodes, "a decision that names no episodes changes none")
	assert.Empty(t, p.Freed)

	held.Episodes, held.Freed = holds, freed
	v.Import = map[types.UID]lifecycle.ImportDecision{types.UID(e.UID): held}
	p = lifecycle.Decide(v)
	assert.Equal(t, holds, only(p).Episodes)
	assert.Equal(t, freed, p.Freed)
	assert.Equal(t, catalogv1alpha1.ImportPhaseHeld, only(p).Import.Phase, "its import is decided as before")
}
