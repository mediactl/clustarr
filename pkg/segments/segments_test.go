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

package segments_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/segments"
)

const (
	intro   = segments.KindIntro
	recap   = segments.KindRecap
	credits = segments.KindCredits
	preview = segments.KindPreview
	tidb    = segments.SourceTheIntroDB
	chap    = segments.SourceChapters
	anal    = segments.SourceAnalysis
)

func seg(k segments.Kind, s, e int64, src segments.Source, c int32) segments.Segment {
	return segments.Segment{Kind: k, StartMs: s, EndMs: e, Source: src, Confidence: c}
}

func TestChapterNames(t *testing.T) {
	// A chapter whose whole title is a credits name is 100 (it outranks
	// TheIntroDB); any other match is 90.
	tests := []struct {
		title string
		kind  segments.Kind
		ok    bool
		conf  int32
	}{
		{"Opening", intro, true, 90},
		{"OP", intro, true, 90},
		{"Intro", intro, true, 90},
		{"Opening End", "", false, 0},
		{"Recap", recap, true, 90},
		{"Previously on", recap, true, 90},
		{"Ending", credits, true, 100},
		{"ED", credits, true, 90},
		{"End Credits", credits, true, 100},
		{" credits ", credits, true, 100},
		{"Credit", credits, true, 100},
		{"Outro", credits, true, 100},
		{"Credits Song", credits, true, 90},
		{"Preview", preview, true, 90},
		{"Next Episode", preview, true, 90},
		{"Chapter 3", "", false, 0},
		{"Operation", "", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			got := segments.FromChapters([]commonv1.Chapter{{Title: tt.title, StartMillis: 1000, EndMillis: 31000}}, false, 0)
			if !tt.ok {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, seg(tt.kind, 1000, 31000, chap, tt.conf), got[0])
		})
	}
}

func TestCreditsCombination(t *testing.T) {
	const dur = 2_700_000 // 45 min
	tests := []struct {
		name    string
		movie   bool
		cands   []segments.Segment
		preview int64
		want    *segments.Segment
	}{
		{
			"two signals within 5 s: the earlier start at 90", false,
			[]segments.Segment{seg(credits, 2_610_000, dur, anal, 70), seg(credits, 2_613_000, dur, anal, 80)},
			0,
			&segments.Segment{Kind: credits, StartMs: 2_610_000, EndMs: dur, Source: anal, Confidence: 90},
		},
		{
			"a single 70 stands", false,
			[]segments.Segment{seg(credits, 2_610_000, dur, anal, 70)},
			0,
			&segments.Segment{Kind: credits, StartMs: 2_610_000, EndMs: dur, Source: anal, Confidence: 70},
		},
		{"a lone 60 is not written", false, []segments.Segment{seg(credits, 2_610_000, dur, anal, 60)}, 0, nil},
		{
			"a chapter wins at 100", false,
			[]segments.Segment{seg(credits, 2_600_000, dur, chap, 100), seg(credits, 2_610_000, dur, anal, 90)},
			0,
			&segments.Segment{Kind: credits, StartMs: 2_600_000, EndMs: dur, Source: chap, Confidence: 100},
		},
		{"credits must reach the end", false, []segments.Segment{seg(credits, 2_000_000, 2_100_000, anal, 90)}, 0, nil},
		{
			"or end where a preview starts", false,
			[]segments.Segment{seg(credits, 2_500_000, 2_640_000, anal, 80)},
			2_642_000,
			&segments.Segment{Kind: credits, StartMs: 2_500_000, EndMs: 2_640_000, Source: anal, Confidence: 80},
		},
		{"an episode's credits are at most 450 s", false, []segments.Segment{seg(credits, dur-500_000, dur, anal, 90)}, 0, nil},
		{
			"a movie's at most 900 s", true,
			[]segments.Segment{seg(credits, dur-800_000, dur, anal, 90)},
			0,
			&segments.Segment{Kind: credits, StartMs: dur - 800_000, EndMs: dur, Source: anal, Confidence: 90},
		},
		{"20 minutes is no movie's credits", true, []segments.Segment{seg(credits, dur-1_200_000, dur, anal, 90)}, 0, nil},
		{"under 15 s is none", false, []segments.Segment{seg(credits, dur-10_000, dur, anal, 90)}, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := segments.Credits(dur, tt.movie, false, tt.cands, tt.preview)
			if tt.want == nil {
				assert.False(t, ok, "%+v", got)
				return
			}
			require.True(t, ok)
			assert.Equal(t, *tt.want, got)
		})
	}
}

func TestAnimePreviewAfterCredits(t *testing.T) {
	p, ok := segments.AnimePreview(seg(credits, 1_300_000, 1_390_000, anal, 90), 1_420_000)
	require.True(t, ok)
	assert.Equal(t, seg(preview, 1_390_000, 1_420_000, anal, 70), p)
	_, ok = segments.AnimePreview(seg(credits, 1_300_000, 1_417_000, anal, 90), 1_420_000)
	assert.False(t, ok, "credits within 5 s of the end leave no preview")
}

func TestMergePrecedence(t *testing.T) {
	got := segments.Merge(
		[]segments.Segment{seg(intro, 60_000, 90_000, tidb, 100)},
		[]segments.Segment{
			seg(intro, 61_000, 92_000, anal, 90),
			seg(credits, 2_600_000, 2_700_000, anal, 90),
			seg(recap, 0, 50_000, chap, 100),
			seg(recap, 0, 40_000, anal, 70),
			seg(preview, 2_690_000, 2_700_000, anal, 55),
		})
	assert.Equal(t, []segments.Segment{
		seg(recap, 0, 50_000, chap, 100),
		seg(intro, 60_000, 90_000, tidb, 100),
		seg(credits, 2_600_000, 2_700_000, anal, 90),
	}, got, "TheIntroDB's intro, analysis credits beside it, chapters over analysis, nothing under 60")

	// Credits: a chapter titled as credits outranks TheIntroDB (Arcane
	// S01E07: "Credits" at 36:18, TheIntroDB 38:21); a pattern-only chapter
	// ("ED") does not, but still outranks analysis.
	assert.Equal(t, []segments.Segment{seg(credits, 2_178_000, 2_403_000, chap, 100)},
		segments.Merge(
			[]segments.Segment{seg(credits, 2_301_000, 2_403_000, tidb, 100)},
			[]segments.Segment{seg(credits, 2_178_000, 2_403_000, chap, 100), seg(credits, 2_300_000, 2_403_000, anal, 90)}),
		"an exact credits chapter over TheIntroDB")
	assert.Equal(t, []segments.Segment{seg(credits, 2_301_000, 2_403_000, tidb, 100)},
		segments.Merge(
			[]segments.Segment{seg(credits, 2_301_000, 2_403_000, tidb, 100)},
			[]segments.Segment{seg(credits, 2_178_000, 2_403_000, chap, 90)}),
		"TheIntroDB over a pattern-only chapter")
	assert.Equal(t, []segments.Segment{seg(credits, 2_178_000, 2_403_000, chap, 90)},
		segments.Merge(nil, []segments.Segment{seg(credits, 2_178_000, 2_403_000, chap, 90), seg(credits, 2_300_000, 2_403_000, anal, 90)}),
		"a pattern-only chapter over analysis")

	many := make([]segments.Segment, 30)
	for i := range many {
		many[i] = seg(credits, int64(i)*1000, int64(i)*1000+500, tidb, 100)
	}
	assert.Len(t, segments.Merge(many, nil), 20)
}

// An anime episode's ED ends before its preview; with no preview chapter
// the credits are accepted when they end within the last 3 minutes, and
// the preview follows them. Anything else must still reach the end.
func TestAnimeCreditsMayEndBeforeAPreview(t *testing.T) {
	const dur = 1_420_000
	ed := seg(credits, 1_300_000, 1_390_000, anal, 80)
	got, ok := segments.Credits(dur, false, true, []segments.Segment{ed}, 0)
	require.True(t, ok)
	assert.Equal(t, ed, got)
	p, ok := segments.AnimePreview(got, dur)
	require.True(t, ok)
	assert.EqualValues(t, 1_390_000, p.StartMs)

	_, ok = segments.Credits(dur, false, false, []segments.Segment{ed}, 0)
	assert.False(t, ok, "not anime: credits reach the end")
	_, ok = segments.Credits(dur, false, true, []segments.Segment{seg(credits, 1_000_000, 1_100_000, anal, 80)}, 0)
	assert.False(t, ok, "five minutes early is not an ED before a preview")
}

// Chapter names are trusted by kind: a movie's chapters give credits only
// ("Opening Night" is a scene), and a preview chapter must lie after the
// midpoint ("Teaser" is a cold open at the start).
func TestChapterRulesByKind(t *testing.T) {
	ch := []commonv1.Chapter{
		{Title: "Opening Night", StartMillis: 0, EndMillis: 600_000},
		{Title: "Teaser", StartMillis: 0, EndMillis: 120_000},
		{Title: "End Credits", StartMillis: 6_600_000, EndMillis: 7_000_000},
		{Title: "Preview", StartMillis: 6_950_000, EndMillis: 7_000_000},
	}
	movie := segments.FromChapters(ch, true, 7_000_000)
	require.Len(t, movie, 1)
	assert.Equal(t, credits, movie[0].Kind)

	ep := segments.FromChapters(ch, false, 7_000_000)
	kinds := map[segments.Kind]int64{}
	for _, s := range ep {
		kinds[s.Kind] = s.StartMs
	}
	assert.Contains(t, kinds, intro)
	assert.Contains(t, kinds, credits)
	assert.EqualValues(t, 6_950_000, kinds[preview], "only the preview after the midpoint")
}
