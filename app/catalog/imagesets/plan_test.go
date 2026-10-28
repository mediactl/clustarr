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

package imagesets_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/imagesets"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

var now = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func seriesInput() imagesets.Input {
	return imagesets.Input{
		Item:   schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "media", Name: "cyberpunk-edgerunners", UID: "u1"}},
		Inputs: schema.ImageSetInputs{Kind: "series", TVDBID: "384541", Seasons: []int32{1}, Episodes: map[string]int32{"1": 10}},
		Mode:   catalogv1alpha1.ImageSetAuto, ProviderReady: true, Now: now,
	}
}

func answeredRecord(in imagesets.Input, at time.Time, ans *schema.ImageSetAnswer, failure string) *schema.ImageSetRecord {
	rec := &schema.ImageSetRecord{Inputs: in.Inputs, Answer: ans}
	item := in.Item
	rec.RecordHeader.Schema, rec.Item, rec.Seq, rec.State, rec.RequestedAt = imagesets.RecordSchema, &item, 7, records.StateAnswered, at.Add(-time.Minute)
	rec.AnsweredAt = &at
	if failure != "" {
		rec.State, rec.Failure = records.StateFailed, failure
	}
	return rec
}

func TestAnItemWithNoChoiceIsAskedOnce(t *testing.T) {
	in := seriesInput()
	d := imagesets.Plan(in)
	require.NotNil(t, d.Request, "a first ask")
	assert.Equal(t, records.StateRequested, d.Request.State)
	assert.Equal(t, "384541", d.Request.Inputs.TVDBID)
	assert.Equal(t, "u1", d.Request.Item.UID)
	assert.Equal(t, now.Add(imagesets.RequestTimeout), d.Due)
	assert.Nil(t, d.ImageSet)

	// The request in flight: nothing more until it times out.
	in.Record, in.RecordOK = d.Request, true
	in.Now = now.Add(time.Minute)
	d = imagesets.Plan(in)
	assert.Nil(t, d.Request)
	assert.NotNil(t, d.Republish, "an unclaimed request is published again inside the window")
	assert.Equal(t, now.Add(imagesets.RequestTimeout), d.Due)

	in.Now = now.Add(imagesets.RequestTimeout + time.Second)
	d = imagesets.Plan(in)
	require.NotNil(t, d.Request, "timed out: asked again")
	assert.NotZero(t, d.Request.Seq, "a new sequence")
}

func TestAnAnswerIsIncorporatedAndReconsideredAtTheRefresh(t *testing.T) {
	in := seriesInput()
	updated := now.Add(-48 * time.Hour)
	in.Record, in.RecordOK = answeredRecord(in, now.Add(-time.Second), &schema.ImageSetAnswer{
		Provider: "mediux", SetID: "41471", Title: "Cyberpunk: Edgerunners", Creator: "sharivari",
		URL: "https://mediux.io/show-set/41471", UpdatedAt: &updated, CheckedAt: now.Add(-time.Second),
		Images: []schema.ImageSetImage{
			{Type: "poster", URL: "https://images.mediux.io/assets/p?key=jpg", SetID: "41471"},
			{Type: "fanart", URL: "https://images.mediux.io/assets/b?key=jpg", SetID: "21971"},
			{Type: "poster", Scope: schema.ImageScopeSeason, Season: 0, URL: "https://images.mediux.io/assets/s0?key=jpg", SetID: "41471"},
			{Type: "poster", Scope: schema.ImageScopeSeason, Season: 1, URL: "https://images.mediux.io/assets/s1?key=jpg", SetID: "41471"},
			{Type: "screenshot", Scope: schema.ImageScopeEpisode, Season: 1, Episode: 3, URL: "https://images.mediux.io/assets/e3?key=jpg", SetID: "41471"},
		},
		Sets: []schema.ImageSetSource{
			{SetID: "41471", Title: "Cyberpunk: Edgerunners", Creator: "sharivari", Kind: "show", URL: "https://mediux.io/show-set/41471", Images: 4},
			{SetID: "21971", Title: "Cyberpunk: Edgerunners Set", Creator: "creshaytions", Kind: "show", URL: "https://mediux.io/show-set/21971", Images: 1},
		},
	}, ""), true
	d := imagesets.Plan(in)
	require.NotNil(t, d.ImageSet)
	assert.True(t, d.Answered)
	assert.Nil(t, d.Request)
	assert.Equal(t, "41471", d.ImageSet.SetID)
	assert.Equal(t, "sharivari", d.ImageSet.Creator)
	assert.Equal(t, "https://mediux.io/show-set/41471", d.ImageSet.URL)
	assert.Equal(t, []catalogv1alpha1.ArtworkOverride{
		{Type: catalogv1alpha1.ImageTypePoster, URL: "https://images.mediux.io/assets/p?key=jpg"},
		{Type: catalogv1alpha1.ImageTypeFanart, URL: "https://images.mediux.io/assets/b?key=jpg"},
	}, d.ImageSet.Artwork, "the specials poster is no second item poster")
	require.Len(t, d.ImageSet.SeasonArtwork, 3)
	assert.Equal(t, catalogv1alpha1.SeasonArtworkOverride{Season: 0, Type: catalogv1alpha1.ImageTypePoster, URL: "https://images.mediux.io/assets/s0?key=jpg"}, d.ImageSet.SeasonArtwork[0])
	assert.Equal(t, int32(1), d.ImageSet.SeasonArtwork[1].Season)
	assert.Nil(t, d.ImageSet.SeasonArtwork[1].Episode)
	assert.Equal(t, int32(3), *d.ImageSet.SeasonArtwork[2].Episode)
	assert.Equal(t, catalogv1alpha1.ImageTypeScreenshot, d.ImageSet.SeasonArtwork[2].Type)
	assert.Equal(t, []catalogv1alpha1.ImageSetSource{
		{SetID: "41471", Title: "Cyberpunk: Edgerunners", Creator: "sharivari", Kind: catalogv1alpha1.ImageSetKindShow, URL: "https://mediux.io/show-set/41471", Images: 4},
		{SetID: "21971", Title: "Cyberpunk: Edgerunners Set", Creator: "creshaytions", Kind: catalogv1alpha1.ImageSetKindShow, URL: "https://mediux.io/show-set/21971", Images: 1},
	}, d.ImageSet.Sets)
	assert.Equal(t, now.Add(-time.Second).Add(imagesets.DefaultRefresh), d.Due, "reconsidered a week on")

	// Stored: the same answer is not incorporated twice, and nothing is
	// asked before the refresh.
	in.Prev = d.ImageSet
	in.Now = now.Add(3 * 24 * time.Hour)
	d = imagesets.Plan(in)
	assert.False(t, d.Answered)
	assert.Nil(t, d.Request)
	assert.Same(t, in.Prev, d.ImageSet)

	// The provider's own refresh, shorter, asks sooner.
	in.Refresh = 2 * 24 * time.Hour
	d = imagesets.Plan(in)
	assert.NotNil(t, d.Request, "past the provider's refresh: asked again")
}

func TestNotFoundAndFailureAnswers(t *testing.T) {
	in := seriesInput()
	in.Record, in.RecordOK = answeredRecord(in, now, &schema.ImageSetAnswer{Provider: "mediux", NotFound: true, CheckedAt: now}, ""), true
	d := imagesets.Plan(in)
	require.NotNil(t, d.ImageSet)
	assert.True(t, d.ImageSet.NotFound)
	assert.Empty(t, d.ImageSet.Artwork)
	assert.Equal(t, now.Add(imagesets.DefaultRefresh), d.Due, "a show with no set is asked again at the refresh, and the daily content list answers that for free")

	// A failure keeps the images of the check before and retries sooner.
	prev := &catalogv1alpha1.ImageSetStatus{
		SetID: "41471", CheckedAt: metav1.NewTime(now.Add(-8 * 24 * time.Hour)),
		Artwork: []catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: "https://images.mediux.io/assets/p"}},
	}
	in.Prev = prev
	in.Record = answeredRecord(in, now, nil, "mediux: 502")
	d = imagesets.Plan(in)
	require.NotNil(t, d.ImageSet)
	assert.Equal(t, "41471", d.ImageSet.SetID, "the images stand")
	assert.Equal(t, "mediux: 502", d.ImageSet.Message)
	assert.Equal(t, now, d.ImageSet.CheckedAt.Time)
	assert.Equal(t, now.Add(imagesets.ErrorRetry), d.Due)
	assert.NotSame(t, prev, d.ImageSet)
}

func TestOptOutClearsAndNoProviderAsksNothing(t *testing.T) {
	in := seriesInput()
	in.Mode = catalogv1alpha1.ImageSetNone
	in.Prev = &catalogv1alpha1.ImageSetStatus{SetID: "41471", CheckedAt: metav1.NewTime(now)}
	d := imagesets.Plan(in)
	assert.True(t, d.Clear)
	assert.Nil(t, d.ImageSet)
	assert.Nil(t, d.Request)

	in = seriesInput()
	in.ProviderReady = false
	d = imagesets.Plan(in)
	assert.Nil(t, d.Request, "no mediux provider: nothing is asked")
	assert.True(t, d.Due.IsZero())

	in = seriesInput()
	in.Inputs = schema.ImageSetInputs{Kind: "movie"}
	assert.Nil(t, imagesets.Plan(in).Request, "a movie with no TMDB id cannot be asked")
	assert.False(t, imagesets.Askable(in.Inputs))
	assert.True(t, imagesets.Askable(schema.ImageSetInputs{Kind: "movie", TMDBID: "328387"}))
}

func TestAnAnswerForAnotherItemIsNotTaken(t *testing.T) {
	in := seriesInput()
	other := in
	other.Inputs.TVDBID = "1"
	in.Record, in.RecordOK = answeredRecord(other, now, &schema.ImageSetAnswer{
		Provider: "mediux", SetID: "9", CheckedAt: now,
		Images: []schema.ImageSetImage{{Type: "poster", URL: "https://x/p"}},
	}, ""), true
	d := imagesets.Plan(in)
	assert.False(t, d.Answered)
	require.NotNil(t, d.Request, "the record is for other ids: asked afresh")
	assert.Equal(t, "384541", d.Request.Inputs.TVDBID)
}

func TestTaskMessageNamesTheItem(t *testing.T) {
	in := seriesInput()
	d := imagesets.Plan(in)
	subject, id, env, err := imagesets.TaskMessage(*d.Request, now)
	require.NoError(t, err)
	assert.Contains(t, subject, "clustarr.work.catalog.imageset.normal.")
	assert.Equal(t, "imageset/u1/"+itoa(d.Request.Seq), id)
	assert.Equal(t, "media/cyberpunk-edgerunners", env.Key)
	var task schema.ImageSetTask
	require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
	assert.Equal(t, "u1", task.Item.UID)
	assert.Equal(t, d.Request.Seq, task.Seq)
	assert.Equal(t, int32(10), task.Inputs.Episodes["1"])
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestADeferredRecordWaitsForItsResetThenResumes(t *testing.T) {
	in := seriesInput()
	until := now.Add(2 * time.Hour)
	rec := &schema.ImageSetRecord{Inputs: in.Inputs}
	item := in.Item
	rec.RecordHeader.Schema, rec.Item, rec.Seq, rec.State, rec.RequestedAt = imagesets.RecordSchema, &item, 3, records.StateDeferred, now.Add(-time.Minute)
	rec.DeferredUntil = &until
	in.Record, in.RecordOK = rec, true
	d := imagesets.Plan(in)
	assert.Nil(t, d.Request)
	assert.Nil(t, d.Resume)
	assert.Equal(t, until, d.Due, "wait for MediUX's reset")

	in.Now = until.Add(time.Minute)
	d = imagesets.Plan(in)
	assert.Nil(t, d.Request)
	require.NotNil(t, d.Resume, "the reset has come: the task is published again")
	assert.Equal(t, int64(3), d.Resume.Seq)
	subject, id, _, err := imagesets.ResumeMessage(*d.Resume, in.Now)
	require.NoError(t, err)
	assert.Contains(t, subject, "clustarr.work.catalog.imageset.normal.")
	assert.Equal(t, "imageset/u1/3/at-"+itoa(until.Unix()), id, "its own Msg-Id, past the first publish's")

	in.Now = until.Add(imagesets.RequestTimeout + time.Second)
	d = imagesets.Plan(in)
	require.NotNil(t, d.Request, "unanswered past the timeout: asked afresh")
}

func TestAPinnedSetIsAskedForByItselfAndAChangedPinAsksAgain(t *testing.T) {
	in := seriesInput()
	in.Inputs = schema.ImageSetInputs{Kind: "series", SetID: "38985"}
	assert.True(t, imagesets.Askable(in.Inputs), "a pin needs no TMDB or TheTVDB id")
	d := imagesets.Plan(in)
	require.NotNil(t, d.Request)
	assert.Equal(t, "38985", d.Request.Inputs.SetID)

	// Answered for the pin; then the owner pins another: asked again at once.
	in.Record, in.RecordOK = answeredRecord(in, now, &schema.ImageSetAnswer{
		Provider: "mediux", SetID: "38985", CheckedAt: now,
		Images: []schema.ImageSetImage{{Type: "poster", URL: "https://x/p"}},
	}, ""), true
	d = imagesets.Plan(in)
	require.True(t, d.Answered)
	in.Prev = d.ImageSet
	in.Inputs.SetID = "41471"
	d = imagesets.Plan(in)
	assert.False(t, d.Answered)
	require.NotNil(t, d.Request, "another pin is another item to the record")
	assert.Equal(t, "41471", d.Request.Inputs.SetID)
}
