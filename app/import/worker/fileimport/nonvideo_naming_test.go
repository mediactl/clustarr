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

package fileimport

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

const namingNS = "media"

func namingMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: namingNS, Name: name}
}

func namingDate(y int, m time.Month, d int) *metav1.Time {
	t := metav1.NewTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	return &t
}

func namingProfile(name string, kind catalogv1alpha1.ProfileMediaKind, tier string) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: kind, Cutoff: tier, Tiers: []catalogv1alpha1.Tier{{Name: tier, Qualities: []string{tier}}},
		},
	}
}

// nonVideoNamingFixture is one namespace with a root of each non-video kind
// and one item tree per kind, none with a resolved status.path, so every
// folder is the kind's preset: the Artist "Boards of Canada" with the
// two-track Album "Music Has the Right to Children", the Author "Terry
// Pratchett" with "Guards! Guards!", the standalone Book "Dune" with a root
// of its own, the Audiobook "Guards! Guards!" (12 parts on disk already,
// which the import cannot know) and the Comic "Saga", a TPB run, with
// issue 001.
func nonVideoNamingFixture(t *testing.T) client.Client {
	t.Helper()
	root := func(name, path string, kind catalogv1alpha1.RootFolderKind) *catalogv1alpha1.RootFolder {
		return &catalogv1alpha1.RootFolder{ObjectMeta: namingMeta(name), Spec: catalogv1alpha1.RootFolderSpec{Path: path, Kind: kind}}
	}
	objs := []client.Object{
		root("music", "/data/music", catalogv1alpha1.RootFolderKindMusic),
		root("books", "/data/books", catalogv1alpha1.RootFolderKindBook),
		root("books2", "/data/books2", catalogv1alpha1.RootFolderKindBook),
		root("audiobooks", "/data/audiobooks", catalogv1alpha1.RootFolderKindAudiobook),
		root("comics", "/data/comics", catalogv1alpha1.RootFolderKindComic),
		namingProfile("lossless", catalogv1alpha1.ProfileMediaKindMusic, "FLAC"),
		namingProfile("ebook", catalogv1alpha1.ProfileMediaKindBook, "EPUB"),
		namingProfile("m4b", catalogv1alpha1.ProfileMediaKindAudiobook, "M4B"),
		namingProfile("cbz", catalogv1alpha1.ProfileMediaKindComic, "CBZ"),
		&catalogv1alpha1.Artist{
			ObjectMeta: namingMeta("boc"),
			Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb-boc", RootFolderRef: "music", QualityProfileRef: "lossless"},
			Status:     catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Boards of Canada"}},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: namingMeta("mhtrtc"),
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "boc", ReleaseGroupID: "rg-1"},
			Status: catalogv1alpha1.AlbumStatus{
				Metadata: &catalogv1alpha1.AlbumMetadata{Title: "Music Has the Right to Children", ReleaseDate: namingDate(1998, time.April, 20)},
				Tracks: []catalogv1alpha1.Track{
					{RecordingID: "rec-1", Medium: 1, Number: 1, Title: "Wildlife Analysis"},
					{RecordingID: "rec-3", Medium: 1, Number: 3, Title: "Telephasic Workshop"},
				},
			},
		},
		&catalogv1alpha1.Author{
			ObjectMeta: namingMeta("pratchett"),
			Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL1A", RootFolderRef: "books", QualityProfileRef: "ebook"},
			Status:     catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: "Terry Pratchett"}},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: namingMeta("guards"),
			Spec:       catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett"), WorkID: "OL1W"},
			Status:     catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: "Guards! Guards!", ReleaseDate: namingDate(1989, time.August, 1)}},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: namingMeta("dune"),
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL2W", RootFolderRef: ptr.To("books2"), QualityProfileRef: ptr.To("ebook")},
			Status:     catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: "Dune"}},
		},
		&catalogv1alpha1.Audiobook{
			ObjectMeta: namingMeta("guards-audio"),
			Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B000", RootFolderRef: "audiobooks", QualityProfileRef: "m4b"},
			Status: catalogv1alpha1.AudiobookStatus{PartCount: 12, Metadata: &catalogv1alpha1.AudiobookMetadata{
				Title:     "Guards! Guards!",
				Authors:   []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}},
				Narrators: []string{"Nigel Planer"},
				Series:    &catalogv1alpha1.SeriesLink{Series: "Discworld", Position: "8"},
			}},
		},
		&catalogv1alpha1.Comic{
			ObjectMeta: namingMeta("saga"),
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1", RootFolderRef: "comics", QualityProfileRef: "cbz",
				SpecialVersion: catalogv1alpha1.ComicSpecialVersionTPB,
			},
			Status: catalogv1alpha1.ComicStatus{Metadata: &catalogv1alpha1.ComicMetadata{Title: "Saga", Year: 2012}},
		},
		&catalogv1alpha1.Issue{ObjectMeta: namingMeta("saga-001"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "001"}},
	}
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).Build()
}

// TestImportPlacesNonVideoFilesWhereTheRenameWould is the non-video kinds'
// TestImportAndRenameRenderTheSamePath (2026-11-10 design D7, §5.5, §6; plan
// W1.4's leftover): for the same objects, the import's destination -- the
// inspect's resolveNonVideo and dest -- is the path the remediation loop's
// naming planner renders for the MediaFile the execute materialises there
// (mediafile.LoadNaming and RenderNaming), so a fresh import reads
// NamingCurrent True and the loop renames nothing. The source names are
// what a download carries -- a disc folder, an upper-case extension, a name
// of its own -- none of which survives.
func TestImportPlacesNonVideoFilesWhereTheRenameWould(t *testing.T) {
	c := nonVideoNamingFixture(t)
	w := &Worker{Client: c, Catalogue: catalogue.LoadedCatalogue()}
	for _, tc := range []struct {
		name   string
		target importtarget.ImportTarget
		rel    string
		frozen schema.FrozenFields
		mi     *commonv1.MediaInfo
		want   string
	}{
		{
			name:   "an album track by its attributed track",
			target: importtarget.ImportTarget{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc"},
			rel:    "CD1/03 - telephasic_workshop.FLAC", frozen: schema.FrozenFields{Track: "rec-3"},
			mi:   &commonv1.MediaInfo{Container: "flac"},
			want: "/data/music/Boards of Canada/Music Has the Right to Children (1998)/Boards of Canada - Music Has the Right to Children - 03 - Telephasic Workshop.flac",
		},
		{
			name:   "an author's book",
			target: importtarget.ImportTarget{Kind: commonv1.MediaKindBook, Name: "guards"},
			rel:    "Pratchett - Guards Guards (retail).epub",
			mi:     &commonv1.MediaInfo{Container: "epub"},
			want:   "/data/books/Terry Pratchett/Guards! Guards!/Terry Pratchett.epub",
		},
		{
			name:   "a standalone book under its own root",
			target: importtarget.ImportTarget{Kind: commonv1.MediaKindBook, Name: "dune"},
			rel:    "dune.PDF",
			mi:     &commonv1.MediaInfo{Container: "pdf", PageCount: 412},
			want:   "/data/books2/Dune/Dune.pdf",
		},
		{
			name:   "an audiobook part by its part index",
			target: importtarget.ImportTarget{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"},
			rel:    "Guards Guards - Part 07.m4b", frozen: schema.FrozenFields{PartIndex: 7},
			mi:   &commonv1.MediaInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2"},
			want: "/data/audiobooks/Terry Pratchett/Discworld/8 - Guards! Guards! Nigel Planer/8 - Guards! Guards! Nigel Planer (07).m4b",
		},
		{
			name:   "an issue of a trade paperback run",
			target: importtarget.ImportTarget{Kind: commonv1.MediaKindIssue, Name: "saga-001"},
			rel:    "Saga 001 (2012) (Digital).cbz",
			mi:     &commonv1.MediaInfo{Container: "cbz", PageCount: 160},
			want:   "/data/comics/Saga/Saga c001 TPB.cbz",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := w.resolveNonVideo(t.Context(), namingNS, tc.target, "")
			require.NoError(t, err)
			f := &schema.InspectedFile{Path: filepath.Join("/downloads/x", tc.rel), Frozen: tc.frozen}
			dest := plan.dest(t.Context(), f, tc.rel, "Some.Release.Title-GRP")
			assert.Equal(t, tc.want, dest, "the import's destination")

			// The MediaFile the execute materialises at dest, as the loop
			// names it.
			ref := plan.ref
			ref.Track = tc.frozen.Track
			mf := &catalogv1alpha1.MediaFile{
				ObjectMeta: namingMeta("file"),
				Spec: catalogv1alpha1.MediaFileSpec{
					MediaRef: ref, Path: dest, PartIndex: tc.frozen.PartIndex,
					ImportedFrom: &catalogv1alpha1.ImportSource{ReleaseTitle: "Some.Release.Title-GRP"},
				},
			}
			look := mediafile.LoadNaming(t.Context(), c, mf)
			n, retry := mediafile.RenderNaming(mf, look, tc.mi, nil, dest, false, false)
			require.NotNil(t, n)
			assert.False(t, retry)
			assert.Empty(t, n.Reason)
			assert.Equal(t, dest, n.ExpectedPath, "the loop's canonical path")
			assert.True(t, n.Current, "a fresh import is at its canonical path")
		})
	}
}

// TestANonVideoFileTheRenameCannotNameKeepsItsOwnName: where the import
// lacks an input the loop has, the file is placed as it was before the
// design -- its own name under the item's folder -- and the loop proposes
// the canonical path on its first pass: an album file attributed to no
// track (held for a person anyway), a track its Album's release does not
// list, and an item whose metadata has not arrived behind a resolved
// status.path.
func TestANonVideoFileTheRenameCannotNameKeepsItsOwnName(t *testing.T) {
	c := nonVideoNamingFixture(t)
	w := &Worker{Client: c, Catalogue: catalogue.LoadedCatalogue()}
	album := importtarget.ImportTarget{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc"}
	plan, err := w.resolveNonVideo(t.Context(), namingNS, album, "")
	require.NoError(t, err)
	folder := "/data/music/Boards of Canada/Music Has the Right to Children (1998)"
	assert.Equal(t, folder, plan.folder)
	for _, tc := range []struct {
		name  string
		track string
	}{
		{"attributed to no track", ""},
		{"a track not on the release", "rec-99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &schema.InspectedFile{Path: "/downloads/x/CD1/07 - Unknown.flac", Frozen: schema.FrozenFields{Track: tc.track}}
			assert.Equal(t, folder+"/CD1/07 - Unknown.flac", plan.dest(t.Context(), f, "CD1/07 - Unknown.flac", ""))
		})
	}

	t.Run("metadata behind a resolved path", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
			&catalogv1alpha1.RootFolder{ObjectMeta: namingMeta("audiobooks"), Spec: catalogv1alpha1.RootFolderSpec{Path: "/data/audiobooks", Kind: catalogv1alpha1.RootFolderKindAudiobook}},
			namingProfile("m4b", catalogv1alpha1.ProfileMediaKindAudiobook, "M4B"),
			&catalogv1alpha1.Audiobook{
				ObjectMeta: namingMeta("ab"),
				Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "audiobooks", QualityProfileRef: "m4b"},
				Status:     catalogv1alpha1.AudiobookStatus{Path: "/data/audiobooks/Someone/Something"},
			},
		).Build()
		w := &Worker{Client: c, Catalogue: catalogue.LoadedCatalogue()}
		plan, err := w.resolveNonVideo(t.Context(), namingNS, importtarget.ImportTarget{Kind: commonv1.MediaKindAudiobook, Name: "ab"}, "")
		require.NoError(t, err)
		f := &schema.InspectedFile{Path: "/downloads/x/part 02.m4b", Frozen: schema.FrozenFields{PartIndex: 2}}
		assert.Equal(t, "/data/audiobooks/Someone/Something/part 02.m4b", plan.dest(t.Context(), f, "part 02.m4b", ""),
			"no title, no preset: the part keeps its name under the resolved folder")
	})
}

// TestTwoTracksRenderedAlikeKeepTheirOwnNames: the track preset has no
// medium token, so two discs' same-numbered, same-titled tracks render one
// path; importplan would refuse the second and hold the album, so both keep
// their own names and the loop proposes the canonical path for each. A
// third track whose path is its own keeps it.
func TestTwoTracksRenderedAlikeKeepTheirOwnNames(t *testing.T) {
	files := []schema.InspectedFile{
		{Path: "/dl/CD1/01 - Intro.flac", Dest: "/data/music/A/B/A - B - 01 - Intro.flac"},
		{Path: "/dl/CD2/01 - Intro.flac", Dest: "/data/music/A/B/A - B - 01 - Intro.flac"},
		{Path: "/dl/CD1/02 - Song.flac", Dest: "/data/music/A/B/A - B - 02 - Song.flac"},
		{Path: "/dl/cover.jpg"},
	}
	keepOwnNamesOnCollision(files, func(f *schema.InspectedFile) string {
		return "/data/music/A/B/own/" + filepath.Base(filepath.Dir(f.Path)) + "/" + filepath.Base(f.Path)
	})
	assert.Equal(t, "/data/music/A/B/own/CD1/01 - Intro.flac", files[0].Dest)
	assert.Equal(t, "/data/music/A/B/own/CD2/01 - Intro.flac", files[1].Dest)
	assert.Equal(t, "/data/music/A/B/A - B - 02 - Song.flac", files[2].Dest)
	assert.Empty(t, files[3].Dest, "a file with no destination is not one")
}
