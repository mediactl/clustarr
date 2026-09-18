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

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRenderFamilyProducesTheLoaderShapeCanonically pins the exact byte
// output render family produces for a small, representative format: two
// conditions (one required ReleaseTitle with a backslash-heavy regex that
// must not be HTML-escaped, one negated+required Resolution), a two-app
// trashIds map and a single-entry scores map -- the same shape as the real
// x265-hd format in data/formats/unwanted.json. This is deliberately a
// *canonical* shape (every field own line, conditions always one per line)
// rather than a byte-for-byte pin of Phase B's hand-compacted style, which
// this task's report documents was not itself internally consistent.
func TestRenderFamilyProducesTheLoaderShapeCanonically(t *testing.T) {
	formats := []resolvedFormat{
		{
			Slug: "x265-hd",
			Name: "x265 (HD)",
			TrashIDs: []manifestApp{
				{App: "radarr", TrashID: "dc98083864ea246d05a42df0d05f81cc"},
				{App: "sonarr", TrashID: "47435ece6b99a0b477caf360e79ba0bb"},
			},
			Scores: []scoreEntry{{Key: "default", Value: -10000}},
			Conditions: []resolvedCondition{
				{Kind: "ReleaseTitle", Name: "x265/HEVC", Required: true, HasPattern: true, Pattern: `[xh][ ._-]?265|\bHEVC(\b|\d)`},
				{Kind: "Resolution", Name: "Not 2160p", Negate: true, Required: true, HasResolution: true, Resolution: 2160},
			},
		},
	}

	want := "[\n" +
		"  {\n" +
		"    \"slug\": \"x265-hd\",\n" +
		"    \"name\": \"x265 (HD)\",\n" +
		"    \"trashIds\": {\"radarr\": \"dc98083864ea246d05a42df0d05f81cc\", \"sonarr\": \"47435ece6b99a0b477caf360e79ba0bb\"},\n" +
		"    \"scores\": {\"default\": -10000},\n" +
		"    \"conditions\": [\n" +
		"      {\"kind\": \"ReleaseTitle\", \"name\": \"x265/HEVC\", \"required\": true, \"pattern\": \"[xh][ ._-]?265|\\\\bHEVC(\\\\b|\\\\d)\"},\n" +
		"      {\"kind\": \"Resolution\", \"name\": \"Not 2160p\", \"negate\": true, \"required\": true, \"resolution\": 2160}\n" +
		"    ]\n" +
		"  }\n" +
		"]\n"

	got := string(renderFamily(formats))
	require.Equal(t, want, got)
}

// TestRenderFamilyOmitsEmptyGroupButKeepsEmptyScores matches the real
// data/'s convention: "group" is left out entirely when a format has none
// (most formats), but "scores" is always present, even as "{}", because
// pkg/quality/catalogue.Format.Scores is read unconditionally by
// pkg/quality's score-set resolution.
func TestRenderFamilyOmitsEmptyGroupButKeepsEmptyScores(t *testing.T) {
	formats := []resolvedFormat{
		{
			Slug:     "anime-dual-audio",
			Name:     "Anime Dual Audio",
			TrashIDs: []manifestApp{{App: "radarr", TrashID: "4a3b087eea2ce012fcc1ce319259a3be"}},
			Scores:   nil,
			Conditions: []resolvedCondition{
				{Kind: "Language", Name: "Japanese Language", HasValue: true, Value: "Japanese"},
			},
		},
	}
	got := string(renderFamily(formats))
	require.Contains(t, got, "\"scores\": {},\n")
	require.NotContains(t, got, "\"group\"")
}

// TestRenderFamilyEmitsGroupWhenSet covers the "group" field's presence and
// its position: after "scores", before "conditions" -- matching
// formatJSON's own field order in load.go.
func TestRenderFamilyEmitsGroupWhenSet(t *testing.T) {
	formats := []resolvedFormat{
		{
			Slug:     "season-pack",
			Name:     "Season Pack",
			Group:    "seasonPack",
			TrashIDs: []manifestApp{{App: "sonarr", TrashID: "3bc5f395426614e155e585a2f056cdf1"}},
			Scores:   []scoreEntry{{Key: "default", Value: 10}},
			Conditions: []resolvedCondition{
				{Kind: "ReleaseType", Name: "Season Packs", HasValue: true, Value: "seasonPack"},
			},
		},
	}
	got := string(renderFamily(formats))
	require.Contains(t, got, "\"scores\": {\"default\": 10},\n    \"group\": \"seasonPack\",\n")
}
