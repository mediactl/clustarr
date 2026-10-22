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

package projection

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

var t0 = time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)

func mediaFile(name string, phase catalogv1.TranscodePhase, at time.Time) *catalogv1.MediaFile {
	mf := &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, UID: types.UID("uid-" + name)},
		Spec: catalogv1.MediaFileSpec{
			Path:     "/data/media/tv/Monster/Season 01/" + name + ".mkv",
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "monster-" + name},
		},
	}
	if phase != "" {
		ts := metav1.NewTime(at)
		mf.Status.Transcode = &catalogv1.TranscodeState{
			Phase: phase, Profile: "hevc-mkv", Class: catalogv1.TranscodeClassIntel,
			Reason: "Reason" + name, Message: "message " + name,
			PlannedAt: &ts, StartedAt: &ts, FinishedAt: &ts,
		}
	}
	return mf
}

func grab(id string, phase commonv1.DownloadPhase, at time.Time) DownloadRow {
	return DownloadRow{
		Owner:     schema.ItemRef{Namespace: "media", Name: "monster"},
		OwnerKind: commonv1.MediaKindSeries,
		Entry: catalogv1.DownloadEntry{
			ID: id, UID: "uid-" + id, Phase: phase, GrabbedAt: metav1.NewTime(at),
			Release: catalogv1.DownloadRelease{Title: "Monster.S01E" + id},
		},
	}
}

func TestTheDashboardCountsEveryTranscodePhaseAndDownloadPhase(t *testing.T) {
	files := []*catalogv1.MediaFile{
		mediaFile("a", catalogv1.TranscodePhaseRunning, t0),
		mediaFile("b", catalogv1.TranscodePhaseSwapping, t0),
		mediaFile("c", catalogv1.TranscodePhaseQueued, t0),
		mediaFile("d", catalogv1.TranscodePhasePlanned, t0),
		mediaFile("e", catalogv1.TranscodePhasePending, t0),
		mediaFile("f", catalogv1.TranscodePhaseFailed, t0),
		mediaFile("g", catalogv1.TranscodePhaseSkipped, t0),
		mediaFile("h", catalogv1.TranscodePhaseSucceeded, t0),
		mediaFile("i", "", t0), // never planned: no transcode at all
	}
	rows := []DownloadRow{
		grab("01", commonv1.DownloadPhaseDownloading, t0),
		grab("02", commonv1.DownloadPhaseQueued, t0),
		grab("03", commonv1.DownloadPhaseAssigned, t0),
		grab("04", commonv1.DownloadPhaseFailed, t0),
		grab("05", commonv1.DownloadPhaseBlocklisted, t0),
		grab("06", commonv1.DownloadPhaseImported, t0),
	}

	d := buildDashboard(context.Background(), files, rows, nil)

	tc := d.Transcodes
	assert.Equal(t, 2, tc.Running.Total, "Running and Swapping")
	assert.Equal(t, 3, tc.Pending.Total, "Pending, Planned and Queued")
	assert.Equal(t, 1, tc.Failed.Total)
	assert.Equal(t, 1, tc.Skipped.Total)
	assert.Equal(t, 1, tc.Succeeded)
	assert.Equal(t, []string{"a", "b"}, transcodeNames(tc.Running.Rows))

	dl := d.Downloads
	assert.Equal(t, 1, dl.Active.Total)
	assert.Equal(t, 2, dl.Pending.Total, "Queued and Assigned")
	assert.Equal(t, 2, dl.Failed.Total, "Failed and Blocklisted")
	assert.Equal(t, 1, dl.Imported)
	assert.Equal(t, "01", dl.Active.Rows[0].Entry.ID)
}

func TestTheDashboardsListsAreCappedNewestFailureFirst(t *testing.T) {
	var files []*catalogv1.MediaFile
	for i := range DashboardRows + 5 {
		files = append(files, mediaFile(fmt.Sprintf("f%02d", i), catalogv1.TranscodePhaseFailed, t0.Add(time.Duration(i)*time.Minute)))
	}

	d := buildDashboard(context.Background(), files, nil, nil)

	require.Len(t, d.Transcodes.Failed.Rows, DashboardRows)
	assert.Equal(t, DashboardRows+5, d.Transcodes.Failed.Total)
	assert.Equal(t, "f12", d.Transcodes.Failed.Rows[0].File.Name, "the newest failure first")
}

func TestARunningTranscodeCarriesItsProgressAndItsItem(t *testing.T) {
	files := []*catalogv1.MediaFile{mediaFile("a", catalogv1.TranscodePhaseRunning, t0)}
	progress := func(_ context.Context, uid types.UID) (int32, bool, error) { return 42, uid == "uid-a", nil }

	d := buildDashboard(context.Background(), files, nil, progress)

	r := d.Transcodes.Running.Rows[0]
	assert.Equal(t, int32(42), r.Percent)
	assert.Equal(t, "a.mkv", r.Name)
	assert.Equal(t, "/library/media/episode/monster-a", r.ItemURL)
	assert.Equal(t, t0, r.Since)
	assert.Equal(t, "message a", r.Message)
}

func TestATranscodeWithoutProgressReadsMinusOne(t *testing.T) {
	d := buildDashboard(context.Background(), []*catalogv1.MediaFile{mediaFile("a", catalogv1.TranscodePhaseQueued, t0)}, nil, nil)

	assert.Equal(t, int32(-1), d.Transcodes.Pending.Rows[0].Percent)
}

func transcodeNames(rows []TranscodeRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.File.Name
	}
	return out
}
