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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
)

// A Comic's live volume pack removes the automatic single-issue grabs of
// the issues it names, still transferring (2026-11-10 design D10: Issues
// get Episodes' pack rules), as a season pack does its episodes' singles.
// A single of an issue the pack does not name, a person's pick, one already
// complete and one grabbed after the pack are kept; nothing is blocklisted.
func TestALiveVolumePackRemovesTheIssueSinglesItReplaces(t *testing.T) {
	n := 0
	grab := func(id string, phase commonv1.DownloadPhase, ago time.Duration, issues ...string) catalogv1alpha1.DownloadEntry {
		n++
		e := entry(phase)
		e.ID, e.UID = id, fmt.Sprintf("6a1b2c3d-0000-4000-8000-0000000003%02d", n)
		e.Release.GUID, e.Release.Title = "guid-"+id, "Saga."+id+".2012.cbz"
		e.Release.InfoHash = fmt.Sprintf("%040d", n)
		e.Issues = issues
		e.GrabbedAt = metav1.NewTime(now.Add(-ago))
		e.RemoveDataOnDelete = false
		return e
	}
	pack := grab("pack", commonv1.DownloadPhaseDownloading, time.Hour, "1", "2", "3", "4")
	covered := grab("i01", commonv1.DownloadPhaseDownloading, 2*time.Hour, "1")
	queued := grab("i2q", commonv1.DownloadPhaseQueued, 2*time.Hour, "2")
	outside := grab("i05xx", commonv1.DownloadPhaseDownloading, 2*time.Hour, "5")
	picked := grab("i03pick", commonv1.DownloadPhaseDownloading, 2*time.Hour, "3")
	picked.Manual = true
	done := grab("i04done", commonv1.DownloadPhaseCompleted, 2*time.Hour, "4")
	later := grab("i1later", commonv1.DownloadPhaseDownloading, time.Minute, "1")

	entries := []catalogv1alpha1.DownloadEntry{pack, covered, queued, outside, picked, done, later}
	var transfers []lifecycle.Transfer
	for _, e := range entries {
		stage := commonv1.DownloadStageTransferring
		if e.Phase == commonv1.DownloadPhaseCompleted {
			stage = commonv1.DownloadStageDone
		}
		transfers = append(transfers, record(e, seq0, stage))
	}
	v := view(entries, transfers...)
	v.Kind = commonv1.MediaKindComic
	p := lifecycle.Decide(v)

	for _, id := range []string{"i01", "i2q"} {
		e := entryNamed(p, id)
		require.NotNil(t, e, id)
		assert.Equal(t, commonv1.DownloadPhaseRemoving, e.Phase, "%s is superseded", id)
		assert.True(t, e.RemoveDataOnDelete, "%s's data goes", id)
		assert.Contains(t, e.Message, "superseded by the volume pack")
	}
	for _, id := range []string{"pack", "i05xx", "i03pick", "i1later", "i04done"} {
		e := entryNamed(p, id)
		require.NotNil(t, e, id)
		assert.NotEqual(t, commonv1.DownloadPhaseRemoving, e.Phase, "%s is kept", id)
	}
	assert.Empty(t, p.Blocks, "nothing is blocklisted")
	assert.Contains(t, reasons(p), lifecycle.ReasonSupersededByPack)
}
