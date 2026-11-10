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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

var a0 = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func atAgo(d time.Duration) *metav1.Time { t := metav1.NewTime(a0.Add(d)); return &t }

func activityFile(name string, kind commonv1.MediaKind) *catalogv1.MediaFile {
	mf := &catalogv1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, CreationTimestamp: *atAgo(-48 * time.Hour)}}
	mf.Spec.Path = "/data/tv/" + name + ".mkv"
	mf.Spec.MediaRef = commonv1.MediaRef{Kind: kind, Name: name}
	mf.Status.ProbeHash = "h-" + name
	return mf
}

func fileNames(l FileList) []string {
	var out []string
	for _, r := range l.Rows {
		out = append(out, r.File.Name)
	}
	return out
}

// TestSubtitlesSortIntoTheirTabs: a file is in one tab -- Searching, Wanted
// (waiting its next search, soonest first), Failed (Blocked, or a language
// whose fetch failed), Done -- and NotWanted is none.
func TestSubtitlesSortIntoTheirTabs(t *testing.T) {
	sub := func(name string, phase catalogv1.SubtitlesPhase, items ...catalogv1.SubtitleItemStatus) *catalogv1.MediaFile {
		mf := activityFile(name, commonv1.MediaKindEpisode)
		mf.Status.Subtitles = &catalogv1.SubtitlesStatus{Phase: phase, Profile: "english", Items: items}
		return mf
	}
	files := []*catalogv1.MediaFile{
		sub("searching", catalogv1.SubtitlesPhaseSearching, catalogv1.SubtitleItemStatus{
			LangKey: "en", State: catalogv1.SubtitleStateSearching, Dispatch: &catalogv1.Dispatch{Seq: 2, AnsweredSeq: 1, DispatchedAt: *atAgo(-time.Minute)},
		}),
		sub("later", catalogv1.SubtitlesPhaseWanted, catalogv1.SubtitleItemStatus{LangKey: "en", State: catalogv1.SubtitleStatePending, NextSearchAt: atAgo(6 * time.Hour)}),
		sub("sooner", catalogv1.SubtitlesPhaseWanted, catalogv1.SubtitleItemStatus{LangKey: "fr", State: catalogv1.SubtitleStatePending, NextSearchAt: atAgo(time.Hour)}),
		sub("errored", catalogv1.SubtitlesPhaseWanted, catalogv1.SubtitleItemStatus{
			LangKey: "en", State: catalogv1.SubtitleStateFailed, LastError: "provider 503", Attempts: commonv1.Attempts{Count: 2, Latest: atAgo(-time.Hour)},
		}),
		func() *catalogv1.MediaFile {
			mf := sub("blocked", catalogv1.SubtitlesPhaseBlocked)
			mf.Status.Subtitles.Reason, mf.Status.Subtitles.Message = "NoProfile", "no SubtitleProfile selects this file"
			return mf
		}(),
		sub("done", catalogv1.SubtitlesPhaseSatisfied, catalogv1.SubtitleItemStatus{LangKey: "en", State: catalogv1.SubtitleStateDownloaded, Provider: "opensubtitles", DownloadedAt: atAgo(-3 * time.Hour)}),
		sub("nothing", catalogv1.SubtitlesPhaseNotWanted),
		activityFile("no-block", commonv1.MediaKindEpisode),
	}
	s := buildSubtitles(files)
	assert.Equal(t, []string{"searching"}, fileNames(s.Searching))
	assert.Equal(t, []string{"sooner", "later"}, fileNames(s.Wanted), "the soonest next search first")
	assert.ElementsMatch(t, []string{"errored", "blocked"}, fileNames(s.Failed))
	assert.Equal(t, []string{"done"}, fileNames(s.Satisfied))

	assert.Equal(t, "english · missing fr pending", s.Wanted.Rows[0].Detail)
	assert.Equal(t, a0.Add(time.Hour), s.Wanted.Rows[0].Since)
	for _, r := range s.Failed.Rows {
		switch r.File.Name {
		case "errored":
			assert.Equal(t, "Failed", r.State)
			assert.Equal(t, "en: provider 503", r.Message)
		case "blocked":
			assert.Equal(t, "Blocked", r.State)
			assert.Equal(t, "NoProfile: no SubtitleProfile selects this file", r.Message)
		}
	}
	assert.Equal(t, "english · en (opensubtitles)", s.Satisfied.Rows[0].Detail)
	assert.Equal(t, "/library/media/episode/done", s.Satisfied.Rows[0].ItemURL)
	assert.Equal(t, "done.mkv", s.Satisfied.Rows[0].Name)
}

// TestMarkersSortIntoTheirTabs: a probed movie or episode file is Pending
// until TheIntroDB or the analysis looked, then Found, None found or Error;
// any other kind, or an unprobed file, is in none.
func TestMarkersSortIntoTheirTabs(t *testing.T) {
	found := activityFile("found", commonv1.MediaKindEpisode)
	found.Status.Markers = &catalogv1.FileMarkers{Result: catalogv1.MarkersFound, FetchedAt: *atAgo(-time.Hour), Segments: []catalogv1.MarkerSegment{
		{Kind: catalogv1.MarkerIntro, StartMs: 42_000, EndMs: 132_000},
		{Kind: catalogv1.MarkerCredits, StartMs: 3_670_000, EndMs: 3_720_000, Source: "chapters"},
	}}
	none := activityFile("none", commonv1.MediaKindMovie)
	none.Status.Markers = &catalogv1.FileMarkers{Result: catalogv1.MarkersNotFound, FetchedAt: *atAgo(-2 * time.Hour),
		Analysis: &catalogv1.SegmentAnalysis{Result: catalogv1.MarkersNotFound, AnalyzedAt: *atAgo(-time.Minute)}}
	failed := activityFile("failed", commonv1.MediaKindEpisode)
	failed.Status.Markers = &catalogv1.FileMarkers{Result: catalogv1.MarkersError, Message: "theintrodb: 503", FetchedAt: *atAgo(-time.Hour)}
	pending := activityFile("pending", commonv1.MediaKindEpisode)
	unprobed := activityFile("unprobed", commonv1.MediaKindEpisode)
	unprobed.Status.ProbeHash = ""
	album := activityFile("album", commonv1.MediaKindAlbum)

	m := buildMarkers([]*catalogv1.MediaFile{found, none, failed, pending, unprobed, album})
	assert.Equal(t, []string{"pending"}, fileNames(m.Pending))
	assert.Equal(t, []string{"found"}, fileNames(m.Found))
	assert.Equal(t, []string{"none"}, fileNames(m.NotFound))
	assert.Equal(t, []string{"failed"}, fileNames(m.Error))

	require.Len(t, m.Found.Rows, 1)
	assert.Equal(t, "intro 0:42-2:12 (theintrodb) · credits 1:01:10-1:02:00 (chapters)", m.Found.Rows[0].Detail)
	assert.Equal(t, "TheIntroDB: NotFound · analysis: NotFound", m.NotFound.Rows[0].Detail)
	assert.Equal(t, a0.Add(-time.Minute), m.NotFound.Rows[0].Since, "the later of the fetch and the analysis")
	assert.Equal(t, "theintrodb: 503", m.Error.Rows[0].Message)
}

// TestCompletedDownloadsAreListed: an entry past its transfer -- waiting
// to import, seeding, imported or being removed -- is on the Completed tab,
// the newest first; imported ones are still counted.
func TestCompletedDownloadsAreListed(t *testing.T) {
	row := func(id string, phase commonv1.DownloadPhase, grabbed time.Duration) DownloadRow {
		return DownloadRow{Entry: catalogv1.DownloadEntry{ID: id, Phase: phase, GrabbedAt: *atAgo(grabbed)}}
	}
	d := buildDashboard(t.Context(), nil, []DownloadRow{
		row("imported", commonv1.DownloadPhaseImported, -3*time.Hour),
		row("seeding", commonv1.DownloadPhaseSeeding, -2*time.Hour),
		row("completed", commonv1.DownloadPhaseCompleted, -time.Hour),
		row("removing", commonv1.DownloadPhaseRemoving, -4*time.Hour),
		row("running", commonv1.DownloadPhaseDownloading, -time.Hour),
	}, nil)
	var ids []string
	for _, r := range d.Downloads.Completed.Rows {
		ids = append(ids, r.Entry.ID)
	}
	assert.Equal(t, []string{"completed", "seeding", "imported", "removing"}, ids)
	assert.Equal(t, 1, d.Downloads.Imported)
	assert.Equal(t, 1, d.Downloads.Active.Total)
}
