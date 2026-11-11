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

package projection_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// A folder the scan could not identify (UnmatchedFile.folder, the
// 2026-11-10 non-video libraries design's D16) reaches the Unmatched page
// as a Folder entry carrying the tab and kind its RootFolder's folders are
// added as (D20): a music root's folder is an Artist on the Music tab, a
// book root's an Author on the Books tab, an audiobook root's an
// Audiobook. A file entry, a folder of a RootFolder that is gone and one
// of a root of another kind carry neither, and the projection reads each
// RootFolder once.
func TestAFolderEntryCarriesTheKindItsRootAddsItAs(t *testing.T) {
	seen := metav1.NewTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	root := func(name string, kind catalogv1.RootFolderKind) *catalogv1.RootFolder {
		return &catalogv1.RootFolder{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       catalogv1.RootFolderSpec{Path: "/data/media/" + name, Kind: kind},
		}
	}
	scan := func(name, rootRef string, unmatched ...catalogv1.UnmatchedFile) *catalogv1.LibraryScan {
		for i := range unmatched {
			unmatched[i].SeenAt = metav1.NewTime(seen.Add(-time.Duration(len(name)*10+i) * time.Second))
		}
		return &catalogv1.LibraryScan{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       catalogv1.LibraryScanSpec{RootFolderRef: rootRef},
			Status:     catalogv1.LibraryScanStatus{Unmatched: unmatched},
		}
	}
	nirvana := []string{
		"Nirvana (90s US grunge band) [5b11f4ce-a62d-471e-81fc-a69a8278c7da]",
		"Nirvana (60s band from the UK) [9282c8b4-ca0b-4c6b-b7e3-4f7762dfc4d6]",
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(
		root("music", catalogv1.RootFolderKindMusic),
		root("books", catalogv1.RootFolderKindBook),
		root("audio", catalogv1.RootFolderKindAudiobook),
		root("movies", catalogv1.RootFolderKindMovie),
		scan("music-scan", "music",
			catalogv1.UnmatchedFile{Path: "Nirvana", Reason: "2 artists are named Nirvana", Candidates: nirvana, Folder: true},
			catalogv1.UnmatchedFile{Path: "Unknown/track.flac", Reason: "unrecognised layout"},
		),
		scan("books-scan", "books",
			catalogv1.UnmatchedFile{Path: "Frank Herbert", Reason: "no author", Folder: true}),
		scan("audio-scan", "audio",
			catalogv1.UnmatchedFile{Path: "Frank Herbert/Dune", Reason: "no audiobook", Folder: true}),
		scan("gone-scan", "deleted",
			catalogv1.UnmatchedFile{Path: "Somebody", Reason: "no artist", Folder: true}),
		scan("movies-scan", "movies",
			catalogv1.UnmatchedFile{Path: "Heat (1995)", Reason: "no movie", Folder: true}),
	).Build()

	proj := projection.New(c, time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	var entries []projection.UnmatchedEntry
	require.Eventually(t, func() bool {
		entries = proj.Unmatched(ctx)
		return len(entries) == 6
	}, 2*time.Second, 10*time.Millisecond, "the projection never listed the six entries")
	byPath := map[string]projection.UnmatchedEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}

	artist := byPath["Nirvana"]
	require.True(t, artist.Folder)
	require.Equal(t, projection.TabMusic, artist.Tab)
	require.Equal(t, commonv1.MediaKindArtist, artist.Kind)
	require.Equal(t, nirvana, artist.Candidates)

	require.True(t, byPath["Frank Herbert"].Folder)
	require.Equal(t, projection.TabBooks, byPath["Frank Herbert"].Tab)
	require.Equal(t, commonv1.MediaKindAuthor, byPath["Frank Herbert"].Kind)

	require.Equal(t, projection.TabAudiobooks, byPath["Frank Herbert/Dune"].Tab)
	require.Equal(t, commonv1.MediaKindAudiobook, byPath["Frank Herbert/Dune"].Kind)

	file := byPath["Unknown/track.flac"]
	require.False(t, file.Folder, "a file entry is no folder")
	require.Empty(t, file.Tab)
	require.Empty(t, file.Kind)

	for _, path := range []string{"Somebody", "Heat (1995)"} {
		require.True(t, byPath[path].Folder, path)
		require.Empty(t, byPath[path].Tab, "%s: a RootFolder gone or of another kind adds the folder as nothing", path)
		require.Empty(t, byPath[path].Kind, path)
	}
}

// ParseFolderCandidate reads the rescan's "Name (disambiguation) [id]"
// (D16): the id the last bracketed token, the disambiguation the group that
// closes the name, matched from its closing parenthesis back; a candidate
// with no id is no FolderCandidate.
func TestParseFolderCandidate(t *testing.T) {
	const mbid = "5b11f4ce-a62d-471e-81fc-a69a8278c7da"
	for in, want := range map[string]projection.FolderCandidate{
		"Nirvana (90s US grunge band) [" + mbid + "]": {Name: "Nirvana", Disambiguation: "90s US grunge band", ID: mbid},
		"Nirvana [" + mbid + "]":                      {Name: "Nirvana", ID: mbid},
		"Sunn O))) [" + mbid + "]":                    {Name: "Sunn O)))", ID: mbid},
		"Sunn O))) (US drone band) [" + mbid + "]":    {Name: "Sunn O)))", Disambiguation: "US drone band", ID: mbid},
		"Genesis (band (UK)) [" + mbid + "]":          {Name: "Genesis", Disambiguation: "band (UK)", ID: mbid},
		"(Hed) P.E. [" + mbid + "]":                   {Name: "(Hed) P.E.", ID: mbid},
		"Frank Herbert [OL79034A]":                    {Name: "Frank Herbert", ID: "OL79034A"},
		"  Dune (Frank Herbert) [B002V1OF70]  ":       {Name: "Dune", Disambiguation: "Frank Herbert", ID: "B002V1OF70"},
	} {
		got, ok := projection.ParseFolderCandidate(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	require.Equal(t, "Nirvana (90s US grunge band)", projection.FolderCandidate{Name: "Nirvana", Disambiguation: "90s US grunge band"}.Label())
	require.Equal(t, "Nirvana", projection.FolderCandidate{Name: "Nirvana"}.Label())

	for _, in := range []string{
		"some-movie-2023", // a file entry's candidate: an item name
		"[" + mbid + "]",  // no name
		"Nirvana []",      // no id
		"Nirvana [two words]",
		"Nirvana (grunge)",
	} {
		_, ok := projection.ParseFolderCandidate(in)
		require.False(t, ok, in)
	}
}
