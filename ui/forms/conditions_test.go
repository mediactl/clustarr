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

package forms_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui/forms"
	"github.com/mediactl/clustarr/ui/schema"
)

// Fields and options follow the choices that make them meaningful (owner,
// 2026-11-16: "selecting an option like music or movies limits which fields
// are valid ... filter which input groups and specific inputs display based
// on the selected options").

// conditionsOf lists every condition a kind's overlay states, with where.
func conditionsOf(k forms.Kind) map[string]*forms.When {
	out := map[string]*forms.When{}
	for _, g := range k.Groups {
		if g.When != nil {
			out["group "+g.Title] = g.When
		}
	}
	for p, w := range k.Only {
		out["only "+p] = w
	}
	for p, opts := range k.OptionsWhen {
		for v, w := range opts {
			out["option "+p+"="+v] = w
		}
	}
	for _, s := range k.Secrets {
		for _, key := range s.Keys {
			if key.When != nil {
				out["secret "+s.Path+"."+key.Key] = key.When
			}
		}
	}
	// a reference rule is a function of the object's class: probe it with
	// the classes the page gives that reference's objects
	for p, rule := range k.RefOptions {
		for _, class := range refClasses[k.Refs[p]] {
			if w := rule(class); w != nil {
				out["ref "+p+" class "+class] = w
			}
		}
	}
	return out
}

// refClasses are the classes ui.listChoices gives each reference's options:
// a quality profile's media kind, a root folder's kind, a download client's
// protocol.
var refClasses = map[forms.Ref][]string{
	forms.RefQualityProfiles: {"video", "music", "book", "audiobook", "comic"},
	forms.RefRootFolders:     {"movie", "series", "music", "book", "audiobook", "comic"},
	forms.RefDownloadClients: {"torrent", "usenet"},
}

// watched are the values a watched field can take, nil when it is free
// text: an enum's members, an enum array's, a boolean's two.
func watched(k forms.Kind, root *schema.Field, path string) (values []string, known bool) {
	if k.Choice != nil && path == k.Choice.ControlName() {
		for _, o := range k.Choice.Options {
			values = append(values, o.Value)
		}
		return values, true
	}
	if path == forms.MetaProtocol {
		return []string{"torrent", "usenet"}, true
	}
	f := root.Lookup(path)
	if f == nil {
		return nil, false
	}
	switch {
	case f.Type == schema.TypeBoolean:
		return []string{"true", "false"}, true
	case f.Type == schema.TypeArray && f.Items != nil:
		return f.Items.Enum, true
	}
	return f.Enum, true
}

func TestEveryConditionWatchesARealFieldAndItsRealValues(t *testing.T) {
	for _, k := range forms.Kinds() {
		root := rootOf(t, k)
		for where, cond := range conditionsOf(k) {
			for w := cond; w != nil; w = w.And {
				values, known := watched(k, root, w.Path)
				require.Truef(t, known, "%s: %s watches %q, which the form does not carry", k.Slug, where, w.Path)
				if len(values) == 0 {
					continue // free text: any value may be compared
				}
				for _, v := range w.Values {
					if v == "" {
						continue // unset
					}
					require.Containsf(t, values, v, "%s: %s compares %q with %q, which it can never be", k.Slug, where, w.Path, v)
				}
			}
		}
		for p := range k.Only {
			require.NotNilf(t, root.Lookup(p), "%s: Only names %q, which the schema lacks", k.Slug, p)
		}
		for p, opts := range k.OptionsWhen {
			f := root.Lookup(p)
			require.NotNilf(t, f, "%s: OptionsWhen names %q, which the schema lacks", k.Slug, p)
			enum := f.Enum
			if f.Type == schema.TypeArray {
				enum = f.Items.Enum
			}
			for v := range opts {
				require.Containsf(t, enum, v, "%s: %q has no option %q", k.Slug, p, v)
			}
		}
		for p := range k.RefOptions {
			ref, isRef := k.Refs[p]
			require.Truef(t, isRef, "%s: RefOptions names %q, which is no reference field", k.Slug, p)
			require.NotEmptyf(t, refClasses[ref], "%s: %q chooses from %s, whose options carry no class", k.Slug, p, ref)
		}
		for _, p := range k.Checks {
			f := root.Lookup(p)
			require.NotNilf(t, f, "%s: Checks names %q, which the schema lacks", k.Slug, p)
			require.Equalf(t, schema.TypeArray, f.Type, "%s: %q is no array", k.Slug, p)
			require.NotEmptyf(t, f.Items.Enum, "%s: %q is not an array of an enumerated string", k.Slug, p)
		}
		for _, p := range k.External {
			require.NotNilf(t, root.Lookup(p), "%s: External names %q, which the schema lacks", k.Slug, p)
		}
	}
}

func TestWhenHoldsAsThePageReadsIt(t *testing.T) {
	values := map[string][]string{"kind": {"music"}, "kinds": {"movie", "series"}, "flag": {"true"}}
	read := func(p string) []string { return values[p] }
	for _, tc := range []struct {
		name string
		when *forms.When
		want bool
	}{
		{"nil always holds", nil, true},
		{"one of", &forms.When{Path: "kind", Values: []string{"music", "book"}}, true},
		{"not one of", &forms.When{Path: "kind", Values: []string{"movie"}}, false},
		{"unset is the empty value", &forms.When{Path: "absent", Values: []string{""}}, true},
		{"none of", &forms.When{Path: "kind", Values: []string{"movie", "series"}, Not: true}, true},
		{"none of, but it is one", &forms.When{Path: "kind", Values: []string{"music"}, Not: true}, false},
		{"any of several values", &forms.When{Path: "kinds", Values: []string{"series"}}, true},
		{"none of several values", &forms.When{Path: "kinds", Values: []string{"album", "series"}, Not: true}, false},
		{"and", &forms.When{Path: "kind", Values: []string{"music"}, And: &forms.When{Path: "flag", Values: []string{"true"}}}, true},
		{"and, the second failing", &forms.When{Path: "kind", Values: []string{"music"}, And: &forms.When{Path: "flag", Values: []string{"false"}}}, false},
	} {
		require.Equalf(t, tc.want, tc.when.Holds(read), tc.name)
	}
	require.Equal(t, "kind=music|book", (&forms.When{Path: "kind", Values: []string{"music", "book"}}).Attr())
	require.Equal(t, "a!=x&b=y|", (&forms.When{Path: "a", Values: []string{"x"}, Not: true, And: &forms.When{Path: "b", Values: []string{"y", ""}}}).Attr())
}

// anyControl finds a control by name in any section.
func anyControl(t *testing.T, f *forms.Form, name string) forms.Control {
	t.Helper()
	for _, s := range f.Sections {
		for _, c := range s.Controls {
			if c.Name == name {
				return c
			}
		}
	}
	require.Failf(t, "no control", "%q", name)
	return forms.Control{}
}

// shownFor reports whether a condition holds for one value of one field.
func shownFor(w *forms.When, path, value string) bool {
	return w.Holds(func(p string) []string {
		if p == path {
			return []string{value}
		}
		return nil
	})
}

func TestARootFoldersFieldsFollowItsKind(t *testing.T) {
	k := kind(t, "rootfolders")
	choices := forms.Choices{forms.RefQualityProfiles: {
		{Value: "hd-bluray-web", Label: "hd-bluray-web", Class: "video"},
		{Value: "music-lossless", Label: "music-lossless", Class: "music"},
	}}
	f := forms.Build(k, rootOf(t, k), nil, choices, forms.ModeNew)

	for name, kinds := range map[string][]string{
		"defaults.minimumAvailability": {"movie"},
		"defaults.seriesType":          {"series"},
		"defaults.seasonFolder":        {"series"},
		"defaults.transcodeProfileRef": {"movie", "series"},
		"defaults.region":              {"audiobook"},
		"naming.multiEpisodeStyle":     {"series"},
		"naming.writeNFO":              {"music", "audiobook"},
		"naming.organizeFolders":       {"music", "book", "audiobook", "comic"},
	} {
		c := anyControl(t, f, name)
		require.NotNilf(t, c.When, "%s has no condition", name)
		for _, kind := range []string{"movie", "series", "music", "book", "audiobook", "comic"} {
			require.Equalf(t, slices.Contains(kinds, kind), shownFor(c.When, "kind", kind), "%s for a %s folder", name, kind)
		}
	}
	// what every kind takes carries no condition
	for _, name := range []string{"defaults.qualityProfileRef", "defaults.monitored", "defaults.tags", "naming.dialect", "scanSchedule"} {
		require.Nilf(t, anyControl(t, f, name).When, "%s is every kind's", name)
	}
	// a folder is offered the quality profiles of its own media kind
	qp := anyControl(t, f, "defaults.qualityProfileRef")
	for _, o := range qp.Options {
		switch o.Value {
		case "hd-bluray-web":
			require.True(t, shownFor(o.When, "kind", "movie"))
			require.True(t, shownFor(o.When, "kind", "series"))
			require.False(t, shownFor(o.When, "kind", "music"))
		case "music-lossless":
			require.True(t, shownFor(o.When, "kind", "music"))
			require.False(t, shownFor(o.When, "kind", "movie"))
		case "":
			require.Nil(t, o.When, "the unset choice is always offered")
		}
	}
	// the anime defaults are a video folder's, and a section of their own
	anime := section(t, f, "Anime")
	require.True(t, shownFor(anime.When, "kind", "series"))
	require.False(t, shownFor(anime.When, "kind", "music"))
}

func TestEnsureKeepsNoAnimeDefaultsWithoutAProfile(t *testing.T) {
	k := kind(t, "rootfolders")
	spec := map[string]any{"defaults": map[string]any{"anime": map[string]any{"seriesType": "anime"}, "monitored": true}}
	k.Ensure(spec)
	require.Equal(t, map[string]any{"defaults": map[string]any{"monitored": true}}, spec)

	spec = map[string]any{"defaults": map[string]any{"anime": map[string]any{"qualityProfileRef": "anime-web-1080p"}}}
	k.Ensure(spec)
	require.Contains(t, spec["defaults"], "anime")
}

func TestAnImportListsKindsAreCheckboxesItsProviderLimits(t *testing.T) {
	k := kind(t, "importlists")
	root := rootOf(t, k)
	f := forms.Build(k, root, map[string]any{"kinds": []any{"movie", "series"}, "trakt": map[string]any{"listType": "watchlist"}}, nil, forms.ModeEdit)
	kinds := anyControl(t, f, "kinds")
	require.Equal(t, forms.ControlChecks, kinds.Type)
	require.Equal(t, []string{"movie", "series"}, kinds.Values)
	require.Len(t, kinds.Options, 6)

	offered := func(option, provider, arrKind string) bool {
		for _, o := range kinds.Options {
			if o.Value == option {
				return o.When.Holds(func(p string) []string {
					switch p {
					case "__choice.provider":
						return []string{provider}
					case "arr.kind":
						return []string{arrKind}
					}
					return nil
				})
			}
		}
		t.Fatalf("no option %q", option)
		return false
	}
	// the CRD's own rules: stevenLu yields movies; trakt movies and series;
	// an *arr what its flavour holds; another clustarr anything
	require.True(t, offered("movie", "stevenLu", ""))
	require.False(t, offered("series", "stevenLu", ""))
	require.True(t, offered("series", "trakt", ""))
	require.False(t, offered("album", "trakt", ""))
	require.True(t, offered("album", "arr", "lidarr"))
	require.False(t, offered("movie", "arr", "lidarr"))
	require.True(t, offered("movie", "arr", "radarr"))
	require.False(t, offered("series", "arr", "radarr"))
	require.True(t, offered("comic", "arr", "clustarr"))
	require.True(t, offered("book", "custom", ""))

	// the checked boxes post one name each; Decode reads them as the array
	spec, err := schema.Decode(root, url.Values{"kinds": {"movie", "series"}})
	require.NoError(t, err)
	require.Equal(t, []any{"movie", "series"}, spec["kinds"])

	// a series' defaults show only while series is among the kinds
	st := anyControl(t, f, "defaults.seriesType")
	require.True(t, st.When.Holds(func(string) []string { return []string{"movie", "series"} }))
	require.False(t, st.When.Holds(func(string) []string { return []string{"movie"} }))
}

func TestAnIndexersTorrentFieldsAndClientsFollowItsProtocol(t *testing.T) {
	k := kind(t, "indexers")
	choices := forms.Choices{forms.RefDownloadClients: {
		{Value: "torrent", Label: "torrent (torrent)", Class: "torrent"},
		{Value: "usenet", Label: "usenet (usenet)", Class: "usenet"},
	}}
	f := forms.Build(k, rootOf(t, k), nil, choices, forms.ModeNew)

	protocol := func(generic, definition string) func(string) []string {
		return func(p string) []string {
			switch p {
			case "generic.protocol":
				return []string{generic}
			case forms.MetaProtocol:
				return []string{definition}
			}
			return nil
		}
	}
	seeders := anyControl(t, f, "minimumSeeders")
	require.True(t, seeders.When.Holds(protocol("", "torrent")), "a torrent definition")
	require.True(t, seeders.When.Holds(protocol("torrent", "")), "a generic Torznab upstream")
	require.True(t, seeders.When.Holds(protocol("", "")), "nothing chosen yet")
	require.False(t, seeders.When.Holds(protocol("usenet", "")), "a generic Newznab upstream")
	require.False(t, section(t, f, "Seeding").When.Holds(protocol("usenet", "")))

	for _, o := range anyControl(t, f, "downloadClientRef").Options {
		switch o.Value {
		case "usenet":
			require.False(t, o.When.Holds(protocol("", "torrent")), "a torrent definition is not offered a usenet client")
			require.True(t, o.When.Holds(protocol("usenet", "")))
		case "torrent":
			require.True(t, o.When.Holds(protocol("", "torrent")))
			require.False(t, o.When.Holds(protocol("usenet", "")))
		}
	}
	// the settings are the definition section's to draw, never Other's
	for _, s := range f.Sections {
		for _, c := range s.Controls {
			require.Falsef(t, c.Name == "settings" || strings.HasPrefix(c.Name, "settings."), "%s draws %s", s.Title, c.Name)
		}
	}
	// the API key is a generic upstream's; a definition's credentials are its own
	key := anyControl(t, f, "__secret.secretRef.apikey")
	require.True(t, shownFor(key.When, "definition", ""))
	require.False(t, shownFor(key.When, "definition", "1337x"))
}

func TestEnsureKeepsAChannelsPlexStationWithoutAGuide(t *testing.T) {
	k := kind(t, "iptvchannels")
	// a hand-picked station and nothing else: the Plex override alone
	spec := map[string]any{"epg": map[string]any{"dummy": false, "plex": map[string]any{"station": "abc123", "off": false}}}
	k.Ensure(spec)
	require.Equal(t, map[string]any{"epg": map[string]any{"plex": map[string]any{"station": "abc123"}}}, spec)

	// neither a station nor off is no override, and then no epg at all
	spec = map[string]any{"epg": map[string]any{"dummy": false, "plex": map[string]any{"off": false}}}
	k.Ensure(spec)
	require.NotContains(t, spec, "epg")

	spec = map[string]any{"epg": map[string]any{"dummy": false, "plex": map[string]any{"off": true, "station": "abc123"}}}
	k.Ensure(spec)
	require.Equal(t, map[string]any{"epg": map[string]any{"plex": map[string]any{"off": true}}}, spec)
}
