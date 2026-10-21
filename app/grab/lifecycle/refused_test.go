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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// A release its indexer kept refusing (releaseRefused: nzb-life's "Already
// Downloaded" set again after it ended, five days running for three Naked
// and Afraid grabs, 2026-10-15..20) is given up without a block: the entry
// is Failed, a redownload is searched, and the entry stays RefusedHold as a
// tombstone so the search grabs another release meanwhile -- the planner
// refuses a release a stored entry holds.
func TestARefusedReleaseIsFailedAndHeldWithoutABlock(t *testing.T) {
	e := usenetEntry(commonv1.DownloadPhaseAssigned)
	refused := failedRecord(e, commonv1.DownloadFailureReleaseRefused)
	refused.Record.Stage, refused.Record.OutputPath = "", "" // never fetched, nothing on disk
	v := view([]catalogv1alpha1.DownloadEntry{e}, refused)
	p := lifecycle.Decide(v)
	got := only(p)
	require.NotNil(t, got)
	assert.Equal(t, commonv1.DownloadPhaseFailed, got.Phase)
	assert.Equal(t, commonv1.DownloadFailureReleaseRefused, got.FailureReason)
	assert.Nil(t, got.Block, "never blocklisted")
	assert.Empty(t, p.Blocks)
	assert.Equal(t, []schema.ItemRef{owner()}, lifecycle.Decide(next(v, p)).Redownloads, "a redownload is searched")

	held := func(age time.Duration) lifecycle.View {
		f := *got
		f.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-age))
		gone := removedRecord(f)
		gone.Record.OutputPath = ""
		hv := view([]catalogv1alpha1.DownloadEntry{f}, gone)
		hv.Searched[owner()] = now.Add(-time.Minute)
		return hv
	}
	require.NotNil(t, only(lifecycle.Decide(held(2*time.Hour))), "kept as a tombstone, the search done")
	assert.Empty(t, lifecycle.Decide(held(lifecycle.RefusedHold+time.Minute)).Entries, "dropped once the hold is over")
}
