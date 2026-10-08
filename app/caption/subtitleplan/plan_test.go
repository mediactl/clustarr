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

package subtitleplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/subtitlestore"
)

// answeredDownload is the pass in which a fetch worker's answer arrives: a
// movie file whose "en" item is in flight at seq 7, the record answering it
// with the subtitle the worker wrote, and -- as Gather reads it -- the
// listing that answer asks for, which holds that sidecar.
func answeredDownload(t *testing.T) (Input, schema.SubtitleRecord) {
	t.Helper()
	now := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)
	dispatched := metav1.NewTime(now.Add(-2 * time.Minute))
	const path = "/data/media/movies/Inception (2010)/Inception (2010).mkv"
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "inception-file", UID: "uid-file"},
		Spec: catalogv1alpha1.MediaFileSpec{
			Path: path, MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"},
		},
	}
	mf.Status.ProbeHash = "probe-1"
	mf.Status.MediaInfo = &commonv1.MediaInfo{Audio: []commonv1.AudioStream{{Language: "jpn"}}}
	mf.Status.Subtitles = &catalogv1alpha1.SubtitlesStatus{
		Phase: catalogv1alpha1.SubtitlesPhaseSearching, Profile: "english", ProbeHash: "probe-1", Wanted: []string{"en"},
		Items: []catalogv1alpha1.SubtitleItemStatus{{
			LangKey: "en", State: catalogv1alpha1.SubtitleStateSearching,
			Dispatch:     &catalogv1alpha1.Dispatch{Seq: 7, DispatchedAt: dispatched},
			Attempts:     commonv1.Attempts{Count: 1, Initial: &dispatched, Latest: &dispatched},
			NextSearchAt: &metav1.Time{Time: dispatched.Add(6 * time.Hour)},
		}},
	}
	profile := subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "english", Generation: 1},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Default:   true,
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}},
		},
	}
	answered := now.Add(-time.Minute)
	rec := schema.SubtitleRecord{
		RecordHeader: schema.RecordHeader{
			Seq: 7, State: records.StateAnswered, RequestedAt: dispatched.Time, AnsweredAt: &answered,
		},
		ProbeHash: "probe-1", ProfileGeneration: 1, Verdict: schema.SubtitleVerdictDownloaded,
		Score: 77, ScoreOutOf: 120, Provider: "e2e-dl1sub-os", SubtitleID: "sub-1", Name: "Inception (2010).en.srt",
	}
	in := Input{
		File: mf, ProbeCurrent: true, Item: Item{Exists: true, OriginalLanguage: "ja"},
		Profiles:    []subtitlev1alpha1.SubtitleProfile{profile},
		Records:     map[string]subtitlestore.Entry{"en": {Record: rec, OK: true, Revision: 3}},
		LastListed:  &Listed{Path: path, ProbeHash: "probe-1"},
		BacklogOpen: true, Now: now,
	}
	require.True(t, Needs(in).List, "an incorporated download lists the directory (gate 2)")
	in.Listing = &Listing{Names: []string{"Inception (2010).mkv", "Inception (2010).en.srt"}}
	return in, rec
}

// TestADownloadedSubtitleKeepsItsItem is smoke finding O2: the pass that
// incorporates a fetch worker's download also lists the sidecar the worker
// just wrote, so the language is no longer wanted -- and the item, still
// never downloaded until the answer is applied, was dropped (and its
// answered dispatch withdrawn) before it was incorporated. The block read
// Satisfied with no items, losing the provider, the score and the upgrade
// schedule. The answered language's item records the subtitle it fetched.
func TestADownloadedSubtitleKeepsItsItem(t *testing.T) {
	in, rec := answeredDownload(t)
	d := Plan(in, func(int64) int64 { return 8 })

	require.NotNil(t, d.Block)
	assert.Equal(t, catalogv1alpha1.SubtitlesPhaseSatisfied, d.Block.Phase, "the sidecar on disk satisfies the profile")
	assert.Empty(t, d.Block.Wanted)
	assert.Empty(t, d.Withdraws, "an answered download is incorporated, never withdrawn")
	assert.Empty(t, d.Dispatches, "nothing is searched again")
	require.Len(t, d.Block.Items, 1, "the downloaded language keeps its item")
	it := d.Block.Items[0]
	assert.Equal(t, "en", it.LangKey)
	assert.Contains(t, []catalogv1alpha1.SubtitleState{catalogv1alpha1.SubtitleStateDownloaded, catalogv1alpha1.SubtitleStateUpgradable}, it.State)
	assert.Equal(t, rec.Name, it.Name)
	assert.Equal(t, rec.Provider, it.Provider)
	assert.Equal(t, rec.SubtitleID, it.SubtitleID)
	assert.Equal(t, rec.Score, it.Score)
	assert.Equal(t, rec.ScoreOutOf, it.ScoreOutOf)
	require.NotNil(t, it.DownloadedAt)
	assert.True(t, it.DownloadedAt.Time.Equal(*rec.AnsweredAt))
	require.NotNil(t, it.Dispatch)
	assert.Equal(t, int64(7), it.Dispatch.AnsweredSeq, "the dispatch is answered at its seq")
	assert.Equal(t, "probe-1", d.Block.ProbeHash)

	// The next pass, over what this one wrote, keeps it as it is.
	next := in
	f := *in.File
	f.Status.Subtitles = d.Block
	f.Status.Sidecars = d.Sidecars
	next.File, next.Listing = &f, nil
	next.Now = in.Now.Add(time.Minute)
	again := Plan(next, func(int64) int64 { return 9 })
	require.Len(t, again.Block.Items, 1)
	assert.Equal(t, it.Name, again.Block.Items[0].Name)
	assert.Equal(t, catalogv1alpha1.SubtitlesPhaseSatisfied, again.Block.Phase)
}

// A language the profile no longer wants and that never downloaded is still
// dropped -- an answer that found nothing keeps nothing alive.
func TestAnAnsweredMissIsStillDroppedOnceUnwanted(t *testing.T) {
	in, _ := answeredDownload(t)
	e := in.Records["en"]
	e.Record.Verdict, e.Record.Name, e.Record.Provider, e.Record.SubtitleID, e.Record.Score = schema.SubtitleVerdictUnavailable, "", "", "", 0
	in.Records["en"] = e
	d := Plan(in, func(int64) int64 { return 8 })
	assert.Empty(t, d.Block.Items, "a hand-placed sidecar satisfies the language; the unanswered search leaves no item")
}
