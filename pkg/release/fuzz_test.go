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

package release_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// fuzzKinds is every kind Parse dispatches on, plus "" for ClassifyKind's
// guess: a search result's title is parsed as the kind searched for,
// whatever shape it really has.
var fuzzKinds = []commonv1.MediaKind{
	"",
	commonv1.MediaKindMovie,
	commonv1.MediaKindSeries,
	commonv1.MediaKindEpisode,
	commonv1.MediaKindArtist,
	commonv1.MediaKindAlbum,
	commonv1.MediaKindAuthor,
	commonv1.MediaKindBook,
	commonv1.MediaKindAudiobook,
	commonv1.MediaKindComic,
	commonv1.MediaKindIssue,
}

// FuzzParse holds Parse to never panicking on any title as any kind: one
// panic aborts the search whose results it was parsing. Seeded with the
// fixture corpus and the titles that once panicked.
func FuzzParse(f *testing.F) {
	files, err := filepath.Glob("../../test/data/releases/*.json")
	if err != nil {
		f.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			f.Fatal(err)
		}
		var fixtures []struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(data, &fixtures); err != nil {
			f.Fatal(err)
		}
		for _, fx := range fixtures {
			f.Add(fx.Title)
		}
	}
	for _, title := range []string{
		"Radiohead - OK Computer [FLAC]",
		"Pink Floyd - The Wall (Deluxe Edition)",
		"Daft Punk - Discovery (Remastered) [MP3 320]",
		"Björk - Homogenic [FLAC]",
		"Sigur Rós - ( ) [MP3 320]",
		"Beyoncé-4-WEB-FLAC-2011-GRP",
		"Muse-Drones-WEB-2015-GRP",
	} {
		f.Add(title)
	}

	f.Fuzz(func(t *testing.T, title string) {
		for _, kind := range fuzzKinds {
			p, err := release.Parse(title, release.Options{Kind: kind})
			if err == nil && p == nil {
				t.Fatalf("Parse(%q, %q) returned neither a release nor an error", title, kind)
			}
		}
	})
}
