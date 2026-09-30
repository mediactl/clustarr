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

package pipeline_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/pipeline"
)

func TestStageSettled(t *testing.T) {
	settled := map[pipeline.Stage]bool{
		pipeline.StageMetadataSearching: false,
		pipeline.StageMetadataFound:     true,
		pipeline.StageMetadataSynced:    true,
		pipeline.StageReleaseSearching:  false,
		pipeline.StageReleaseSelected:   false,
		pipeline.StageDownloading:       false,
		pipeline.StageDownloaded:        false,
		pipeline.StageImporting:         false,
		pipeline.StageImported:          true,
		pipeline.StageSubtitleSearching: false,
		pipeline.StageSubtitleFound:     false,
		pipeline.StageSubtitleFetching:  false,
		pipeline.StageSubtitleDone:      true,
		pipeline.StageTranscoding:       false,
		pipeline.StageTranscodeDone:     true,
		pipeline.StageComplete:          true,
		pipeline.StageFailed:            true,
		pipeline.StageBlocked:           true,
	}
	for stage, want := range settled {
		require.Equal(t, want, stage.Settled(), "stage %s", stage)
	}
}

func entries(n int, stage pipeline.Stage, base time.Time) []pipeline.Entry {
	out := make([]pipeline.Entry, n)
	for i := range out {
		out[i] = pipeline.Entry{
			Ref:   types.NamespacedName{Name: fmt.Sprintf("%s-%03d", stage, i)},
			Stage: stage,
			Since: base.Add(time.Duration(i) * time.Minute),
		}
	}
	return out
}

func TestTrimKeepsInFlightAndNewestSettled(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	in := append(entries(150, pipeline.StageComplete, base), entries(5, pipeline.StageDownloading, base)...)

	out := pipeline.Trim(in, 100)

	require.Len(t, out, 105)
	for i, e := range out[:5] {
		require.Equal(t, pipeline.StageDownloading, e.Stage, "in-flight entries come first, entry %d", i)
	}
	settled := out[5:]
	require.Equal(t, base.Add(149*time.Minute), settled[0].Since, "the newest result first")
	require.Equal(t, base.Add(50*time.Minute), settled[99].Since, "the 100th newest result last")
	for i := 1; i < len(settled); i++ {
		require.True(t, settled[i-1].Since.After(settled[i].Since), "results newest first at %d", i)
	}
}

func TestTrimZeroKeepsOnlyInFlight(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	in := append(entries(3, pipeline.StageFailed, base), entries(2, pipeline.StageTranscoding, base)...)
	out := pipeline.Trim(in, 0)
	require.Len(t, out, 2)
	require.Equal(t, pipeline.StageTranscoding, out[0].Stage)
}

// TestProjectSettledSinceIsWhenItSettled: a settled entry reads Since as
// the newest activity it has, not the item's creation -- an item added in
// 2020 and imported yesterday is yesterday's result, so the page's last-X
// trim keeps it ahead of older ones.
func TestProjectSettledSinceIsWhenItSettled(t *testing.T) {
	created := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	completed := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	imported := time.Date(2026, 9, 28, 11, 0, 0, 0, time.UTC)

	book := bookWith(t)
	book.CreationTimestamp = metav1.NewTime(created)
	entry := pipeline.Project(book, pipeline.Related{
		Downloads: []downloadv1.Download{{
			ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(completed.Add(-time.Hour))},
			Status: downloadv1.DownloadStatus{
				Phase:       downloadv1.DownloadPhaseImported,
				CompletedAt: &metav1.Time{Time: completed},
				Import:      &downloadv1.ImportState{ImportedAt: &metav1.Time{Time: imported}},
			},
		}},
		MediaFile: &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{
			Name: book.GetName(), CreationTimestamp: metav1.NewTime(imported.Add(-time.Minute)),
		}},
	})
	require.Equal(t, pipeline.StageComplete, entry.Stage)
	require.Equal(t, imported, entry.Since.UTC())

	idle := bookWith(t)
	idle.CreationTimestamp = metav1.NewTime(created)
	entry = pipeline.Project(idle, pipeline.Related{})
	require.True(t, entry.Stage.Settled(), "an idle item is settled (stage %s)", entry.Stage)
	require.Equal(t, created, entry.Since.UTC(), "an item with no activity settled when it was created")
}
