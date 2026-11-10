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

package plexfilter_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/plexfilter"
)

// recordedFields reads a fixture's Meta -- talos's PMS 1.43.5, 2026-11-10
// -- into fields by Plex type ("movie", "show", "episode").
func recordedFields(t *testing.T, path string) map[string][]plexfilter.Field {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc struct {
		MediaContainer struct {
			Meta struct {
				Type []struct {
					Type  string `json:"type"`
					Field []struct {
						Key   string `json:"key"`
						Title string `json:"title"`
						Type  string `json:"type"`
					} `json:"Field"`
				} `json:"Type"`
			} `json:"Meta"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	out := map[string][]plexfilter.Field{}
	for _, ty := range doc.MediaContainer.Meta.Type {
		for _, f := range ty.Field {
			out[ty.Type] = append(out[ty.Type], plexfilter.Field{Key: f.Key, Title: f.Title, Type: plexfilter.FieldType(f.Type)})
		}
	}
	return out
}

func TestCatalogueMatchesRecordedMeta(t *testing.T) {
	movie := recordedFields(t, "../../test/data/plexfilter/meta-movie.json")
	tv := recordedFields(t, "../../test/data/plexfilter/meta-tv.json")
	require.Len(t, movie["movie"], 39)
	require.Len(t, tv["show"], 20)
	require.Len(t, tv["episode"], 26)

	assert.Equal(t, movie["movie"], plexfilter.Fields(catalogv1.PlexItemMovie))
	tvFields := append(append([]plexfilter.Field{}, tv["show"]...), tv["episode"]...)
	for _, it := range []catalogv1.PlexItemType{catalogv1.PlexItemShow, catalogv1.PlexItemSeason, catalogv1.PlexItemEpisode} {
		assert.Equal(t, tvFields, plexfilter.Fields(it), "item type %s", it)
	}
}

func TestSortsMatchRecordedMeta(t *testing.T) {
	read := func(path string) map[string][]plexfilter.Sort {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var doc struct {
			MediaContainer struct {
				Meta struct {
					Type []struct {
						Type string `json:"type"`
						Sort []struct {
							Key   string `json:"key"`
							Title string `json:"title"`
						} `json:"Sort"`
					} `json:"Type"`
				} `json:"Meta"`
			} `json:"MediaContainer"`
		}
		require.NoError(t, json.Unmarshal(raw, &doc))
		out := map[string][]plexfilter.Sort{}
		for _, ty := range doc.MediaContainer.Meta.Type {
			for _, s := range ty.Sort {
				out[ty.Type] = append(out[ty.Type], plexfilter.Sort{Key: s.Key, Title: s.Title})
			}
		}
		return out
	}
	movie := read("../../test/data/plexfilter/meta-movie.json")
	tv := read("../../test/data/plexfilter/meta-tv.json")
	assert.Equal(t, movie["movie"], plexfilter.Sorts(catalogv1.PlexItemMovie))
	assert.Equal(t, tv["show"], plexfilter.Sorts(catalogv1.PlexItemShow))
	assert.Equal(t, tv["season"], plexfilter.Sorts(catalogv1.PlexItemSeason))
	assert.Equal(t, tv["episode"], plexfilter.Sorts(catalogv1.PlexItemEpisode))
}

func animeDubbed() catalogv1.PlexCollectionSpec {
	return catalogv1.PlexCollectionSpec{
		Library: "Clustarr TV", Title: "Anime Dubbed", ItemType: catalogv1.PlexItemShow,
		Filter: &catalogv1.PlexFilter{Match: catalogv1.PlexMatchAll, Rules: []catalogv1.PlexFilterRule{
			{Field: "episode.audioLanguage", Operator: catalogv1.PlexOpIs, Value: "en"},
			{Field: "show.genre", Operator: catalogv1.PlexOpIs, Value: "Anime"},
		}},
		Sort: &catalogv1.PlexSort{Field: "titleSort"},
	}
}

func netflix() catalogv1.PlexCollectionSpec {
	return catalogv1.PlexCollectionSpec{
		Library: "Clustarr TV", Title: "Netflix", ItemType: catalogv1.PlexItemShow,
		Filter: &catalogv1.PlexFilter{Match: catalogv1.PlexMatchAll, Rules: []catalogv1.PlexFilterRule{
			{Field: "show.network", Operator: catalogv1.PlexOpIs, Value: "Netflix"},
		}},
	}
}

func up() catalogv1.PlexCollectionSpec {
	return catalogv1.PlexCollectionSpec{
		Library: "Clustarr Movies", Title: "UP", ItemType: catalogv1.PlexItemMovie,
		Filter: &catalogv1.PlexFilter{Match: catalogv1.PlexMatchAny, Rules: []catalogv1.PlexFilterRule{
			{Field: "title", Operator: catalogv1.PlexOpIs, Value: "Seven Up!"},
			{Match: catalogv1.PlexMatchAll, Rules: []catalogv1.PlexCondition{
				{Field: "genre", Operator: catalogv1.PlexOpIs, Value: "Documentary"},
				{Field: "title", Operator: catalogv1.PlexOpContains, Value: "Up"},
			}},
		}},
	}
}

func ghibli() catalogv1.PlexCollectionSpec {
	return catalogv1.PlexCollectionSpec{
		Library: "Clustarr Movies", Title: "Studio Ghibli Essentials", ItemType: catalogv1.PlexItemMovie,
		Members: &catalogv1.PlexMembers{Items: []catalogv1.PlexMember{
			{Kind: "Movie", Name: "spirited-away-2001"},
			{Kind: "Movie", Name: "my-neighbor-totoro-1988"},
		}},
		Summary:  ptr.To("Every Ghibli film worth a rewatch."),
		Settings: &catalogv1.PlexCollectionSettings{Order: catalogv1.PlexOrderReleaseDate},
	}
}

func TestValidateAcceptsTheReferenceCollections(t *testing.T) {
	for name, spec := range map[string]catalogv1.PlexCollectionSpec{
		"Anime Dubbed": animeDubbed(), "Netflix": netflix(), "UP": up(), "Ghibli": ghibli(),
	} {
		assert.NoError(t, plexfilter.Validate(spec), name)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*catalogv1.PlexCollectionSpec)
		base   func() catalogv1.PlexCollectionSpec
		path   string
	}{
		{"a tag cannot contain", func(s *catalogv1.PlexCollectionSpec) {
			s.Filter.Rules[0] = catalogv1.PlexFilterRule{Field: "genre", Operator: catalogv1.PlexOpContains, Value: "Doc"}
		}, up, "filter.rules[0]"},
		{"a TV field in a movie collection", func(s *catalogv1.PlexCollectionSpec) {
			s.Filter.Rules[0] = catalogv1.PlexFilterRule{Field: "show.network", Operator: catalogv1.PlexOpIs, Value: "Netflix"}
		}, up, "filter.rules[0]"},
		{"a condition with no value", func(s *catalogv1.PlexCollectionSpec) {
			s.Filter.Rules[0].Value = ""
		}, up, "filter.rules[0]"},
		{"a condition in a group with an unknown field", func(s *catalogv1.PlexCollectionSpec) {
			s.Filter.Rules[1].Rules[1].Field = "nonsense"
		}, up, "filter.rules[1].rules[1]"},
		{"a rule both condition and group", func(s *catalogv1.PlexCollectionSpec) {
			s.Filter.Rules[0].Match = catalogv1.PlexMatchAll
		}, up, "filter.rules[0]"},
		{"33 rules", func(s *catalogv1.PlexCollectionSpec) {
			for len(s.Filter.Rules) < 33 {
				s.Filter.Rules = append(s.Filter.Rules, s.Filter.Rules[0])
			}
		}, netflix, "filter.rules"},
		{"both filter and members", func(s *catalogv1.PlexCollectionSpec) {
			s.Members = ghibli().Members
		}, up, "members"},
		{"a Series in a movie collection", func(s *catalogv1.PlexCollectionSpec) {
			s.Members.Items[0].Kind = "Series"
		}, ghibli, "members.items[0]"},
		{"a duplicate member", func(s *catalogv1.PlexCollectionSpec) {
			s.Members.Items[1] = s.Members.Items[0]
		}, ghibli, "members.items[1]"},
		{"an order on a smart collection", func(s *catalogv1.PlexCollectionSpec) {
			s.Settings = &catalogv1.PlexCollectionSettings{Order: catalogv1.PlexOrderAlphabetical}
		}, up, "settings.order"},
		{"a sort the item type lacks", func(s *catalogv1.PlexCollectionSpec) {
			s.Sort = &catalogv1.PlexSort{Field: "unviewedLeafCount"}
		}, up, "sort.field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.base()
			tc.mutate(&spec)
			err := plexfilter.Validate(spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.path)
		})
	}
}

func TestDescribe(t *testing.T) {
	assert.Equal(t, `Audio Language is en and Genre is Anime`, plexfilter.Describe(animeDubbed()))
	assert.Equal(t, `Network is Netflix`, plexfilter.Describe(netflix()))
	assert.Equal(t, `Title is "Seven Up!" or (Genre is Documentary and Title contains "Up")`, plexfilter.Describe(up()))
	assert.Equal(t, `2 items`, plexfilter.Describe(ghibli()))
}

func TestImageURLAllowed(t *testing.T) {
	const base = "https://clustarr.k8s.appkins.io/art/"
	for _, ok := range []string{
		"https://image.tmdb.org/t/p/original/a.jpg",
		"https://clustarr.k8s.appkins.io/art/x",
		"https://93.184.216.34/a.jpg",
		"https://[64:ff9b::5db8:d822]/a.jpg",
	} {
		assert.NoError(t, plexfilter.ImageURLAllowed(ok, base), ok)
	}
	for _, bad := range []string{
		"http://example.com/a.jpg",
		"https://10.0.0.5/a.jpg",
		"https://[fe80::1]/a.jpg",
		"https://127.0.0.1/a.jpg",
		"https://grafana.observability.svc.cluster.local/x",
		"https://clustarr.k8s.appkins.io/settings",
		"ftp://example.com/a.jpg",
		"https://0.1.2.3/a.jpg",
		"https://198.18.0.1/a.jpg",
		"https://203.0.113.9/a.jpg",
		"https://240.0.0.1/a.jpg",
		"https://100.64.0.1/a.jpg",
		"https://[2001:db8::1]/a.jpg",
		"https://[64:ff9b::a00:1]/a.jpg",
		"https://[2002:a00:1::1]/a.jpg",
	} {
		assert.Error(t, plexfilter.ImageURLAllowed(bad, base), bad)
	}
}
