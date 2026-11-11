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

package lineup_test

import (
	"bufio"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/pkg/iptv/lineup"
)

// loadExample reads the example manifest's spec.lineup through the API type
// and FromSpec, as the lineup controller does.
func loadExample(t *testing.T) lineup.Spec {
	t.Helper()
	b, err := os.ReadFile("../../../docs/examples/iptvprovider-argon-lineup.yaml")
	require.NoError(t, err)
	var p clustarrv1.IPTVProvider
	require.NoError(t, yaml.UnmarshalStrict(b, &p))
	require.NotNil(t, p.Spec.Lineup)
	return lineup.FromSpec(p.Spec.Lineup, int(p.Spec.MaxChannelsOrDefault()))
}

func TestFromSpecKeepsTheRulesInOrderAndTheFirstBase(t *testing.T) {
	assert.Equal(t, lineup.Spec{MaxChannels: 400}, lineup.FromSpec(nil, 400))
	news, sports := int32(1000), int32(2000)
	got := lineup.FromSpec(&clustarrv1.LineupSpec{
		Trim: []string{"^US: "}, Exclude: []string{"ADULT"},
		Categories: []clustarrv1.LineupCategory{
			{Name: "News", Base: &news, Groups: []string{"NEWS"}, Names: []string{"^CNN"}, Exclude: []string{"WEST"}},
			{Name: "Sports", Base: &sports},
			{Name: "News", Groups: []string{"TV"}},
		},
	}, 250)
	assert.Equal(t, lineup.Spec{
		Trim: []string{"^US: "}, Exclude: []string{"ADULT"}, MaxChannels: 250,
		Rules: []lineup.Rule{
			{Category: "News", Base: 1000, Groups: []string{"NEWS"}, Names: []string{"^CNN"}, Exclude: []string{"WEST"}},
			{Category: "Sports", Base: 2000},
			{Category: "News", Groups: []string{"TV"}},
		},
	}, got)
}

func planner(t *testing.T, s lineup.Spec) *lineup.Planner {
	t.Helper()
	p, err := lineup.Compile(s)
	require.NoError(t, err)
	return p
}

func TestTrimNameStripsTheMarkers(t *testing.T) {
	trim := loadExample(t).Trim
	res := compileTrim(t, trim)
	for in, want := range map[string]string{
		"USA: CNN [1080p]":      "CNN",
		"HBO HITS EAST":         "HBO HITS",
		"VIS: FOX SPORTS 1 (E)": "FOX SPORTS 1",
		"US:  A  &  E (BK)":     "A & E",
	} {
		assert.Equal(t, want, lineup.TrimName(in, res), in)
	}
}

func e(key, name, group string) lineup.Entry { return lineup.Entry{Key: key, Name: name, Group: group} }

func newsRule() lineup.Rule {
	return lineup.Rule{Category: "News", Base: 1000, Groups: []string{"NEWS", "TV"}, Names: []string{"^(CNN|BBC|ABC NEWS)"}}
}

func TestDuplicatesPreferHDThenGroupOrderThenPlaylistOrder(t *testing.T) {
	p := planner(t, lineup.Spec{Trim: []string{`\s*\[[^\]]+\]`}, Rules: []lineup.Rule{newsRule()}})
	plan := p.Plan([]lineup.Entry{e("tv", "CNN [720p]", "TV"), e("news", "CNN [1080p]", "NEWS")}, nil)
	require.Len(t, plan.Channels, 1)
	assert.Equal(t, "news", plan.Channels[0].Key)
	require.Len(t, plan.Dropped, 1)
	assert.Equal(t, lineup.Dropped{Key: "tv", Name: "CNN", Reason: "duplicate", DuplicateOf: "news"}, plan.Dropped[0])

	// Equal resolution: the group listed first wins.
	plan = p.Plan([]lineup.Entry{e("tv", "CNN [1080p]", "TV"), e("news", "CNN [1080p]", "NEWS")}, nil)
	assert.Equal(t, "news", plan.Channels[0].Key)
	// Equal everything: playlist order.
	plan = p.Plan([]lineup.Entry{e("a", "CNN", "NEWS"), e("b", "CNN", "NEWS")}, nil)
	assert.Equal(t, "a", plan.Channels[0].Key)
	assert.Equal(t, "a", plan.Dropped[0].DuplicateOf)
	// 4K beats 1080p; the raw name is read, not the trimmed one.
	plan = p.Plan([]lineup.Entry{e("a", "CNN [1080p]", "NEWS"), e("b", "CNN [4K]", "TV")}, nil)
	assert.Equal(t, "b", plan.Channels[0].Key)
}

func TestFirstMatchingRuleWins(t *testing.T) {
	p := planner(t, lineup.Spec{Rules: []lineup.Rule{
		{Category: "Sports", Base: 2000, Names: []string{"^ESPN"}},
		{Category: "News", Base: 1000, Names: []string{"ESPN", "^CNN"}},
	}})
	plan := p.Plan([]lineup.Entry{e("1", "ESPN NEWS", "TV"), e("2", "CNN", "TV")}, nil)
	byKey := map[string]lineup.Assignment{}
	for _, a := range plan.Channels {
		byKey[a.Key] = a
	}
	assert.Equal(t, "Sports", byKey["1"].Category)
	assert.Equal(t, "2001", byKey["1"].Number)
	assert.Equal(t, "1001", byKey["2"].Number)
}

func TestASecondRuleOfANameJoinsTheCategory(t *testing.T) {
	p := planner(t, lineup.Spec{Rules: []lineup.Rule{
		{Category: "News", Base: 1000, Groups: []string{"NEWS"}},
		{Category: "Sports", Base: 2000, Groups: []string{"SPORTS"}},
		{Category: "News", Groups: []string{"TV"}, Names: []string{"^CNN"}},
	}})
	plan := p.Plan([]lineup.Entry{e("1", "BBC", "NEWS"), e("2", "CNN", "TV")}, nil)
	require.Len(t, plan.Categories, 2)
	assert.Equal(t, lineup.Category{Name: "News", Base: 1000, Channels: 2}, plan.Categories[0])
	assert.Equal(t, "Sports", plan.Categories[1].Name)
	assert.Equal(t, 0, plan.Categories[1].Channels)
	assert.Equal(t, "1001", plan.Channels[0].Number)
	assert.Equal(t, "1002", plan.Channels[1].Number)
}

func TestNumbersAreAlphabeticalFromBasePlusOne(t *testing.T) {
	p := planner(t, lineup.Spec{Rules: []lineup.Rule{{Category: "News", Base: 1000}}})
	plan := p.Plan([]lineup.Entry{e("b", "BBC NEWS", "G"), e("c", "CNN", "G"), e("a", "ABC NEWS", "G")}, nil)
	var got []string
	for _, a := range plan.Channels {
		got = append(got, a.Number+" "+a.Name)
	}
	assert.Equal(t, []string{"1001 ABC NEWS", "1002 BBC NEWS", "1003 CNN"}, got)

	plan = p.Plan([]lineup.Entry{e("1", "ESPN", "G"), e("2", "E!", "G")}, nil)
	assert.Equal(t, "E!", plan.Channels[0].Name)
	assert.Equal(t, "ESPN", plan.Channels[1].Name)
}

func TestAnOverrideMovesAndAnEmptyOverrideTurnsOff(t *testing.T) {
	p := planner(t, lineup.Spec{Rules: []lineup.Rule{
		{Category: "News", Base: 1000, Names: []string{"^CNN"}},
		{Category: "Sports", Base: 2000, Names: []string{"^ESPN"}},
	}})
	sports, off := "Sports", ""
	plan := p.Plan(
		[]lineup.Entry{e("1", "CNN", "G"), e("2", "ESPN", "G"), e("3", "ESPN 2", "G")},
		map[string]lineup.Override{"1": {Category: &sports, Name: "Aardvark"}, "3": {Category: &off}},
	)
	require.Len(t, plan.Channels, 2)
	assert.Equal(t, "Aardvark", plan.Channels[0].Name)
	assert.Equal(t, "2001", plan.Channels[0].Number)
	assert.Equal(t, "Sports", plan.Channels[0].Category)
	assert.Equal(t, "2002", plan.Channels[1].Number)
	require.Len(t, plan.Dropped, 1)
	assert.Equal(t, "off", plan.Dropped[0].Reason)
	assert.Equal(t, "3", plan.Dropped[0].Key)
}

func TestTooManyRefusesTheCategory(t *testing.T) {
	p := planner(t, lineup.Spec{MaxChannels: 2, Rules: []lineup.Rule{{Category: "News", Base: 1000, Exclude: []string{"^K"}}, {Category: "Kids", Base: 5000, Names: []string{"^K"}}}})
	plan := p.Plan([]lineup.Entry{e("1", "A", "G"), e("2", "B", "G"), e("3", "C", "G"), e("4", "Kx", "G")}, nil)
	require.Len(t, plan.Categories, 2)
	assert.True(t, plan.Categories[0].TooMany)
	assert.Equal(t, 3, plan.Categories[0].Channels)
	require.Len(t, plan.Channels, 1) // only Kids is assigned
	assert.Equal(t, "Kids", plan.Channels[0].Category)
	require.Len(t, plan.Dropped, 3)
	for _, d := range plan.Dropped {
		assert.Equal(t, "tooMany", d.Reason)
	}
}

func TestInvalidRuleIsAnError(t *testing.T) {
	_, err := lineup.Compile(lineup.Spec{Rules: []lineup.Rule{{Category: "News", Names: []string{"("}}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "News")
	assert.Contains(t, err.Error(), `"("`)
	_, err = lineup.Compile(lineup.Spec{Trim: []string{"["}})
	require.Error(t, err)
}

func TestUnmatchedEntriesAreDroppedNotGuessed(t *testing.T) {
	p := planner(t, lineup.Spec{Exclude: []string{"WEST"}, Rules: []lineup.Rule{{Category: "News", Base: 1000, Groups: []string{"NEWS"}}}})
	plan := p.Plan([]lineup.Entry{e("1", "Mystery", "OTHER"), e("2", "CNN WEST", "NEWS"), e("3", "CNN", "NEWS")}, nil)
	require.Len(t, plan.Channels, 1)
	reasons := map[string]string{}
	for _, d := range plan.Dropped {
		reasons[d.Key] = d.Reason
	}
	assert.Equal(t, map[string]string{"1": "unmatched", "2": "excluded"}, reasons)
}

func TestTheArgonSampleClassifies(t *testing.T) {
	spec := loadExample(t)
	p := planner(t, spec)
	f, err := os.Open("../../../test/data/iptv/lineup_names.txt")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var entries []lineup.Entry
	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan(); i++ {
		g, n, ok := strings.Cut(sc.Text(), "\t")
		require.True(t, ok)
		entries = append(entries, lineup.Entry{Key: "k" + string(rune('A'+i/26/26%26)) + string(rune('A'+i/26%26)) + string(rune('A'+i%26)), Name: n, Group: g})
	}
	require.Greater(t, len(entries), 500)
	plan := p.Plan(entries, nil)

	group := map[string]string{}
	for _, en := range entries {
		group[en.Key] = en.Group
	}
	var newsEx []string
	for _, r := range spec.Rules {
		if r.Category == "News" && len(r.Exclude) > 0 {
			newsEx = append(newsEx, r.Exclude...)
		}
	}
	newsExclude := regexp.MustCompile("(?i)" + strings.Join(newsEx, "|"))
	headers := 0
	for _, d := range plan.Dropped {
		if strings.Contains(d.Name, "✶") {
			headers++
			assert.Equal(t, "excluded", d.Reason, d.Name)
		}
		g := group[d.Key]
		if d.Reason != "unmatched" {
			continue
		}
		switch {
		case strings.Contains(g, "SPORTS NETWORK"), strings.Contains(g, "KIDS NETWORK"):
			t.Errorf("unmatched in %s: %q", g, d.Name)
		case strings.Contains(g, "NEWS NETWORK"):
			// The example's News rule excludes the out-of-market locals of this
			// group on purpose; nothing else of it may be left over.
			if !newsExclude.MatchString(d.Name) {
				t.Errorf("unmatched in %s, and not by the News rule's exclude: %q", g, d.Name)
			}
		}
	}
	assert.Positive(t, headers, "the sample carries group headers")
	counts := map[string]int{}
	for _, d := range plan.Dropped {
		counts["dropped:"+d.Reason]++
	}
	for _, c := range plan.Categories {
		counts[c.Name] = c.Channels
		assert.False(t, c.TooMany, c.Name)
	}
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("%-45s %d", k, counts[k])
	}
}

func compileTrim(t *testing.T, pats []string) []*regexp.Regexp {
	t.Helper()
	var out []*regexp.Regexp
	for _, p := range pats {
		out = append(out, regexp.MustCompile(p))
	}
	return out
}
