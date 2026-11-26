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

package mediafile

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/naming"
)

const nvNS = "media"

func nvMeta(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: nvNS, Name: name} }

func nvDate(y int, m time.Month, d int) *metav1.Time {
	t := metav1.NewTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	return &t
}

// nonVideoFixture is one namespace with a music, a book, an audiobook and a
// comic RootFolder and one item tree of each kind: the Artist "Boards of
// Canada" with the Album "Music Has the Right to Children" (two tracks), the
// Author "Terry Pratchett" with "Guards! Guards!" and a standalone Book
// "Dune" with its own root, the Audiobook "Guards! Guards!" of 12 parts,
// and the Comic "Saga" (a TPB run) with issue 001.
func nonVideoFixture(t *testing.T) client.Client {
	t.Helper()
	root := func(name, path string, kind catalogv1alpha1.RootFolderKind) *catalogv1alpha1.RootFolder {
		return &catalogv1alpha1.RootFolder{ObjectMeta: nvMeta(name), Spec: catalogv1alpha1.RootFolderSpec{Path: path, Kind: kind}}
	}
	objs := []client.Object{
		root("music", "/data/music", catalogv1alpha1.RootFolderKindMusic),
		root("books", "/data/books", catalogv1alpha1.RootFolderKindBook),
		root("books2", "/data/books2", catalogv1alpha1.RootFolderKindBook),
		root("audiobooks", "/data/audiobooks", catalogv1alpha1.RootFolderKindAudiobook),
		root("comics", "/data/comics", catalogv1alpha1.RootFolderKindComic),
		&catalogv1alpha1.Artist{
			ObjectMeta: nvMeta("boc"),
			Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb-boc", RootFolderRef: "music", QualityProfileRef: "lossless"},
			Status:     catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Boards of Canada"}},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: nvMeta("mhtrtc"),
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "boc", ReleaseGroupID: "rg-1"},
			Status: catalogv1alpha1.AlbumStatus{
				Metadata: &catalogv1alpha1.AlbumMetadata{Title: "Music Has the Right to Children", ReleaseDate: nvDate(1998, time.April, 20)},
				Tracks: []catalogv1alpha1.Track{
					{RecordingID: "rec-1", Medium: 1, Number: 1, Title: "Wildlife Analysis"},
					{RecordingID: "rec-3", Medium: 1, Number: 3, Title: "Telephasic Workshop"},
				},
			},
		},
		&catalogv1alpha1.Album{
			ObjectMeta: nvMeta("geogaddi"),
			Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "boc", ReleaseGroupID: "rg-2", QualityProfileRef: ptr.To("any-music")},
			Status:     catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{Title: "Geogaddi"}},
		},
		&catalogv1alpha1.Author{
			ObjectMeta: nvMeta("pratchett"),
			Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL1A", RootFolderRef: "books", QualityProfileRef: "ebook"},
			Status:     catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: "Terry Pratchett"}},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: nvMeta("guards"),
			Spec:       catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett"), WorkID: "OL1W"},
			Status:     catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: "Guards! Guards!", ReleaseDate: nvDate(1989, time.August, 1)}},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: nvMeta("dune"),
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL2W", RootFolderRef: ptr.To("books2"), QualityProfileRef: ptr.To("ebook-any")},
			Status:     catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: "Dune"}},
		},
		&catalogv1alpha1.Book{
			ObjectMeta: nvMeta("rootless"),
			Spec:       catalogv1alpha1.BookSpec{WorkID: "OL3W"},
			Status:     catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: "Rootless"}},
		},
		&catalogv1alpha1.Audiobook{
			ObjectMeta: nvMeta("guards-audio"),
			Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B000", RootFolderRef: "audiobooks", QualityProfileRef: "m4b"},
			Status: catalogv1alpha1.AudiobookStatus{PartCount: 12, Metadata: &catalogv1alpha1.AudiobookMetadata{
				Title:     "Guards! Guards!",
				Authors:   []catalogv1alpha1.NamedRef{{Name: "Terry Pratchett"}},
				Narrators: []string{"Nigel Planer"},
				Series:    &catalogv1alpha1.SeriesLink{Series: "Discworld", Position: "8"},
			}},
		},
		&catalogv1alpha1.Comic{
			ObjectMeta: nvMeta("saga"),
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-1", RootFolderRef: "comics", QualityProfileRef: "cbz",
				SpecialVersion: catalogv1alpha1.ComicSpecialVersionTPB,
			},
			Status: catalogv1alpha1.ComicStatus{Metadata: &catalogv1alpha1.ComicMetadata{Title: "Saga", Year: 2012}},
		},
		&catalogv1alpha1.Issue{ObjectMeta: nvMeta("saga-001"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "001"}},
		&catalogv1alpha1.Issue{ObjectMeta: nvMeta("saga-002"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "002"}},
	}
	// The Audiobook's first six parts, placed by the import, so a seventh
	// file is numbered 7 among them (audiobookPartNumber lists them through
	// the item index, which the loop registers once per process).
	for i := int32(1); i <= 6; i++ {
		objs = append(objs, &catalogv1alpha1.MediaFile{
			ObjectMeta: nvMeta(fmt.Sprintf("guards-audio-%02d", i)),
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"},
				Path:      fmt.Sprintf("/data/audiobooks/Terry Pratchett/Discworld/8 - Guards! Guards! Nigel Planer/8 - Guards! Guards! Nigel Planer (%02d).m4b", i),
				PartIndex: 1000 + i,
			},
		})
	}
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.MediaFile{}, mfindex.Item, mfindex.Extract).Build()
}

func nvFile(path string, ref commonv1.MediaRef, part int32) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: nvMeta("file"),
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: path, PartIndex: part},
	}
}

// TestNamingOwnerOfTheNonVideoKinds is the owner and root resolution
// table (2026-11-10 design D7, §5.5): an Album's file resolves through its
// Artist (the Artist's root, the Album's profile override else the
// Artist's), a Book's through its own rootFolderRef else its Author's, an
// Audiobook's its own, an Issue's through its Comic; and why a file cannot
// be named when it cannot.
func TestNamingOwnerOfTheNonVideoKinds(t *testing.T) {
	c := nonVideoFixture(t)
	for _, tc := range []struct {
		name          string
		ref           commonv1.MediaRef
		wantReason    catalogv1alpha1.NamingReason
		wantRoot      string
		wantProfile   string
		wantNone      bool // nil owner, no reason: a kind this planner does not name
		checkContext  func(t *testing.T, o *namingOwner)
		wantRenderErr bool
	}{
		{
			name: "an album track through its artist", ref: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "rec-3"},
			wantRoot: "music", wantProfile: "lossless",
			checkContext: func(t *testing.T, o *namingOwner) {
				assert.Equal(t, "Boards of Canada", o.context.ArtistName)
				assert.Equal(t, "Music Has the Right to Children", o.context.AlbumTitle)
				assert.Equal(t, 3, o.context.Track)
				assert.Equal(t, "Telephasic Workshop", o.context.TrackTitle)
				assert.Equal(t, 1998, o.context.Year)
			},
		},
		{
			name: "an album's profile override", ref: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "geogaddi", Track: "rec-9"},
			wantReason: catalogv1alpha1.NamingReasonMetadataPending, // its tracks have not arrived
		},
		{
			name: "an album file naming no track", ref: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc"},
			wantReason: catalogv1alpha1.NamingReasonUnrenderable,
		},
		{
			name: "a track not on the album's release", ref: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "rec-99"},
			wantReason: catalogv1alpha1.NamingReasonUnrenderable,
		},
		{
			name: "an album that does not exist", ref: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "nope", Track: "rec-1"},
			wantReason: catalogv1alpha1.NamingReasonMetadataPending,
		},
		{
			name: "an author's book through its author", ref: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "guards"},
			wantRoot: "books", wantProfile: "ebook",
			checkContext: func(t *testing.T, o *namingOwner) {
				assert.Equal(t, "Terry Pratchett", o.context.AuthorName)
				assert.Equal(t, "Guards! Guards!", o.context.BookTitle)
				assert.Equal(t, 1989, o.context.Year)
			},
		},
		{
			name: "a standalone book's own root and profile", ref: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dune"},
			wantRoot: "books2", wantProfile: "ebook-any",
			checkContext: func(t *testing.T, o *namingOwner) {
				assert.Empty(t, o.context.AuthorName)
				assert.Equal(t, "Dune", o.context.BookTitle)
			},
		},
		{
			name: "a standalone book without a root", ref: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "rootless"},
			wantReason: catalogv1alpha1.NamingReasonUnrenderable,
		},
		{
			name: "an audiobook's own root", ref: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"},
			wantRoot: "audiobooks", wantProfile: "m4b",
			checkContext: func(t *testing.T, o *namingOwner) {
				assert.Equal(t, "Terry Pratchett", o.context.AuthorName)
				assert.Equal(t, "Nigel Planer", o.context.Narrator)
				assert.Equal(t, "Discworld", o.context.BookSeries)
				assert.Equal(t, "8", o.context.BookSeriesPosition)
				assert.Equal(t, 7, o.context.PartNumber, "the file's place among the Audiobook's parts, never its key 1007")
				assert.Equal(t, 7, o.context.PartCount, "the parts listed, the file itself counted, never status.partCount")
			},
		},
		{
			name: "an issue through its comic", ref: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001"},
			wantRoot: "comics", wantProfile: "cbz",
			checkContext: func(t *testing.T, o *namingOwner) {
				assert.Equal(t, "Saga", o.context.ComicSeriesTitle)
				assert.Equal(t, "001", o.context.IssueNumber)
				assert.Equal(t, "TPB", o.context.SpecialVersion, "the Comic's spec.specialVersion")
				assert.Equal(t, 2012, o.context.Year)
			},
		},
		{
			name: "a file covering two issues keeps its name", ref: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001", Keys: []string{"saga-001", "saga-002"}},
			wantReason: catalogv1alpha1.NamingReasonUnrenderable,
		},
		{
			name: "an issue whose comic is gone", ref: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "orphan"},
			wantReason: catalogv1alpha1.NamingReasonMetadataPending,
		},
		{
			name: "a kind with no preset", ref: commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "boc"},
			wantNone: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, reason, err := namingOwnerOf(t.Context(), c, nvFile("/data/x/file.bin", tc.ref, 1007))
			require.NoError(t, err)
			if tc.wantNone {
				assert.Nil(t, owner)
				assert.Empty(t, reason)
				return
			}
			if tc.wantReason != "" {
				assert.Nil(t, owner)
				assert.Equal(t, tc.wantReason, reason)
				return
			}
			require.NotNil(t, owner)
			assert.Empty(t, reason)
			assert.Equal(t, tc.wantRoot, owner.rootFolderRef)
			assert.Equal(t, tc.wantProfile, owner.qualityProfileRef)
			assert.Equal(t, tc.ref.Kind, owner.context.Kind)
			if tc.checkContext != nil {
				tc.checkContext(t, owner)
			}
		})
	}
}

// TestRenderNamingForEveryKind renders each kind's canonical path through
// LoadNaming and RenderNaming, as the naming planner does: the import's
// folder rule and the kind's file preset, the file's own extension
// lowercased (never the probed container's), and Current when the file is
// already there.
func TestRenderNamingForEveryKind(t *testing.T) {
	c := nonVideoFixture(t)
	audio := &commonv1.MediaInfo{Container: "flac"}
	m4b := &commonv1.MediaInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2"}
	for _, tc := range []struct {
		name string
		file *catalogv1alpha1.MediaFile
		mi   *commonv1.MediaInfo
		want string
	}{
		{
			"an album track", nvFile("/data/music/Boards of Canada/MHTRTC/03.FLAC", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "rec-3"}, 0), audio,
			"/data/music/Boards of Canada/Music Has the Right to Children (1998)/Boards of Canada - Music Has the Right to Children - 03 - Telephasic Workshop.flac",
		},
		{
			"an author's book", nvFile("/data/books/Terry Pratchett/Guards.epub", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "guards"}, 0), &commonv1.MediaInfo{Container: "epub"},
			"/data/books/Terry Pratchett/Guards! Guards!/Terry Pratchett.epub",
		},
		{
			"a standalone book", nvFile("/data/books2/dune.PDF", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dune"}, 0), &commonv1.MediaInfo{Container: "pdf", PageCount: 412},
			"/data/books2/Dune/Dune.pdf",
		},
		{
			"an audiobook part", nvFile("/data/audiobooks/incoming/guards-07.m4b", commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"}, 1007), m4b,
			"/data/audiobooks/Terry Pratchett/Discworld/8 - Guards! Guards! Nigel Planer/8 - Guards! Guards! Nigel Planer (07).m4b",
		},
		{
			"an issue of a trade paperback run", nvFile("/data/comics/Saga/saga 001.cbz", commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001"}, 0), &commonv1.MediaInfo{Container: "cbz", PageCount: 160},
			"/data/comics/Saga/Saga c001 TPB.cbz",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			look := LoadNaming(t.Context(), c, tc.file)
			require.NoError(t, look.err)
			n, retry := RenderNaming(tc.file, look, tc.mi, nil, tc.file.Spec.Path, false, false)
			require.NotNil(t, n)
			assert.False(t, retry)
			assert.Empty(t, n.Reason)
			assert.Equal(t, tc.want, n.ExpectedPath)
			assert.False(t, n.Current)

			n, _ = RenderNaming(tc.file, look, tc.mi, nil, tc.want, false, false)
			assert.True(t, n.Current, "a file at its canonical path is Current")
			n, _ = RenderNaming(tc.file, look, nil, nil, tc.file.Spec.Path, false, false)
			assert.Equal(t, catalogv1alpha1.NamingReasonProbePending, n.Reason, "no probe, no name")
		})
	}
}

// TestRenderNamingReportsAMissingRootForEveryKind: an item whose RootFolder
// is gone is Unrenderable, as a Movie's is.
func TestRenderNamingReportsAMissingRootForEveryKind(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.Audiobook{
			ObjectMeta: nvMeta("ab"),
			Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "gone"},
			Status:     catalogv1alpha1.AudiobookStatus{Metadata: &catalogv1alpha1.AudiobookMetadata{Title: "Dune"}},
		},
	).WithIndex(&catalogv1alpha1.MediaFile{}, mfindex.Item, mfindex.Extract).Build()
	f := nvFile("/data/audiobooks/dune.m4b", commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "ab"}, 0)
	look := LoadNaming(t.Context(), c, f)
	require.NoError(t, look.err)
	assert.True(t, look.rootMissing)
	n, _ := RenderNaming(f, look, &commonv1.MediaInfo{Container: "mp4"}, nil, f.Spec.Path, false, false)
	require.NotNil(t, n)
	assert.Equal(t, catalogv1alpha1.NamingReasonUnrenderable, n.Reason)
}

// TestAnAudiobookPartIsNumberedAmongItsSiblings is audiobookPartNumber
// (2026-11-10 design D7, D11; task D6): {PartNumber} is the file's place
// among its Audiobook's MediaFiles in play order -- a two-disc set's keys
// 1001, 1002 and 2001 and an unnumbered file are parts 1 to 4, never
// "(2001)" -- and {PartCount} the parts listed, the file counted once when
// the cache has yet to list it; a lone part has no number; and a listing
// that fails is the lookup's error, over which RenderNaming keeps the
// previous proposal and asks for a retry.
func TestAnAudiobookPartIsNumberedAmongItsSiblings(t *testing.T) {
	ref := commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"}
	part := func(name, path string, key int32) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: nvMeta(name), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: path, PartIndex: key}}
	}
	indexed := func(objs ...client.Object) client.Client {
		return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
			WithIndex(&catalogv1alpha1.MediaFile{}, mfindex.Item, mfindex.Extract).Build()
	}
	d1c1, d1c2 := part("d1c1", "/dl/CD1/01.mp3", 1001), part("d1c2", "/dl/CD1/02.mp3", 1002)
	d2c1, loose := part("d2c1", "/dl/CD2/01.mp3", 2001), part("loose", "/dl/Chapter 04.mp3", 0)

	c := indexed(d1c1, d1c2, d2c1, loose)
	for i, mf := range []*catalogv1alpha1.MediaFile{d1c1, d1c2, d2c1, loose} {
		number, count, err := audiobookPartNumber(t.Context(), c, mf)
		require.NoError(t, err)
		assert.Equal(t, int32(i+1), number, "%s is part %d, not part %d", mf.Name, i+1, mf.Spec.PartIndex) //nolint:gosec // test
		assert.Equal(t, 4, count)
	}

	fresh := part("fresh", "/dl/CD2/02.mp3", 2002)
	number, count, err := audiobookPartNumber(t.Context(), c, fresh)
	require.NoError(t, err)
	assert.Equal(t, int32(4), number, "a file the cache has yet to list counts itself: the fourth numbered part, ahead of the unnumbered one")
	assert.Equal(t, 5, count)

	number, count, err = audiobookPartNumber(t.Context(), indexed(d2c1), d2c1)
	require.NoError(t, err)
	assert.Zero(t, number, "a lone part has no number")
	assert.Equal(t, 1, count)

	// Through LoadNaming: the item's fixture lists six parts, so a seventh
	// renders "(07)" of 7, and a List the cache cannot answer keeps the
	// previous proposal (ProbePending here) and asks for a retry.
	seventh := nvFile("/data/audiobooks/incoming/guards-07.m4b", ref, 1007)
	look := LoadNaming(t.Context(), nonVideoFixture(t), seventh)
	require.NoError(t, look.err)
	assert.Equal(t, 7, look.owner.context.PartNumber)
	assert.Equal(t, 7, look.owner.context.PartCount)

	prev := &catalogv1alpha1.NamingStatus{Reason: catalogv1alpha1.NamingReasonProbePending}
	look = LoadNaming(t.Context(), failingList{nonVideoFixture(t)}, seventh)
	require.Error(t, look.err)
	assert.Nil(t, look.owner)
	n, retry := RenderNaming(seventh, look, &commonv1.MediaInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2"}, prev, seventh.Spec.Path, false, false)
	assert.True(t, retry, "a failed listing is retried")
	assert.Equal(t, prev, n, "the previous proposal stands over a listing that failed")
}

// failingList is a reader whose every List fails, as a cache blip would.
type failingList struct{ client.Reader }

func (failingList) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("the cache is not synced")
}

// An album file whose own title tag is another song than the track it is
// attributed to is never named, so the rename pass never moves it under that
// song's title: it reads TrackTitleMismatch until a scan corrects its track
// (2026-11-16: album files attributed by number against the wrong edition,
// about one in twenty on talos). A tag that agrees, or no title tag, names it.
func TestAnAlbumFileWhoseTitleIsAnotherSongIsNotNamed(t *testing.T) {
	c := nonVideoFixture(t)
	file := nvFile("/data/music/Boards of Canada/MHTRTC/03.FLAC", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "rec-3"}, 0)
	look := LoadNaming(t.Context(), c, file)
	require.NoError(t, look.err)
	tagged := func(title string) *commonv1.MediaInfo {
		return &commonv1.MediaInfo{Container: "flac", Tags: &commonv1.MediaTags{Title: title, TrackNumber: 3}}
	}

	n, _ := RenderNaming(file, look, tagged("Roygbiv"), nil, file.Spec.Path, false, false)
	require.NotNil(t, n)
	assert.Equal(t, catalogv1alpha1.NamingReasonTrackTitleMismatch, n.Reason)
	assert.Empty(t, n.ExpectedPath, "no path, so no rename")

	n, _ = RenderNaming(file, look, tagged("Telephasic Workshop"), nil, file.Spec.Path, false, false)
	assert.Empty(t, n.Reason)
	assert.NotEmpty(t, n.ExpectedPath)
	n, _ = RenderNaming(file, look, tagged(""), nil, file.Spec.Path, false, false)
	assert.NotEmpty(t, n.ExpectedPath, "a file without a title tag is named by its track")
}

// flippingLook is a lookup whose renderer names an episode file by its
// release title alone, with next deciding what each title proposes: what
// the format memo's one wrong answer made of One Piece S18E28 on talos.
func flippingLook(next map[string]string) NamingLookup {
	return NamingLookup{
		root: &catalogv1alpha1.RootFolder{},
		owner: &namingOwner{render: func(_ *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
			to, ok := next[c.ReleaseTitle]
			if !ok {
				return "", fmt.Errorf("no name for %q", c.ReleaseTitle)
			}
			return "/data/tv/One Piece/Season 18/" + to + ext, nil
		}},
	}
}

func flippingFile(title string) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{
		Path:     "/data/tv/One Piece/Season 18/" + title + ".mp4",
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "one-piece-s18e28"},
	}}
}

// A file with no recorded release title is named from its own name, so what
// is proposed for it depends on what it is called. A proposal is only a name
// the file would keep: on talos "[v2 v3]" proposed "[v2]" and "[v2]"
// proposed "[v2 v3]", and the file was renamed between them 4.86 million
// times in eight days (2026-11-18 to 2026-11-25).
func TestANameIsProposedOnlyIfTheFileWouldKeepIt(t *testing.T) {
	mi := &commonv1.MediaInfo{Container: "mp4"}
	at := func(title string) string { return "/data/tv/One Piece/Season 18/" + title + ".mp4" }
	render := func(next map[string]string, title string) *catalogv1alpha1.NamingStatus {
		f := flippingFile(title)
		n, retry := RenderNaming(f, flippingLook(next), mi, nil, f.Spec.Path, false, false)
		require.NotNil(t, n)
		require.False(t, retry)
		return n
	}

	t.Run("two names that propose each other: the file stays where it is", func(t *testing.T) {
		round := map[string]string{"S18E28 [v2 v3]": "S18E28 [v2]", "S18E28 [v2]": "S18E28 [v2 v3]"}
		for _, title := range []string{"S18E28 [v2 v3]", "S18E28 [v2]"} {
			n := render(round, title)
			assert.Empty(t, n.Reason, title)
			assert.Equal(t, at(title), n.ExpectedPath, title)
			assert.True(t, n.Current, "%s: nothing is renamed", title)
		}
	})
	t.Run("a name that proposes itself is the answer, in one rename", func(t *testing.T) {
		chain := map[string]string{"a": "b", "b": "c", "c": "c"}
		n := render(chain, "a")
		assert.Equal(t, at("c"), n.ExpectedPath, "the name the file comes to rest at, not the first one proposed")
		assert.False(t, n.Current)
		assert.True(t, render(chain, "c").Current)
	})
	t.Run("a longer round holds the file too", func(t *testing.T) {
		round := map[string]string{"a": "b", "b": "c", "c": "a"}
		n := render(round, "a")
		assert.Equal(t, at("a"), n.ExpectedPath)
		assert.True(t, n.Current)
	})
	t.Run("names that neither settle nor return are no name", func(t *testing.T) {
		n := render(map[string]string{"a": "b", "b": "c", "c": "d", "d": "e", "e": "d"}, "a")
		assert.Equal(t, catalogv1alpha1.NamingReasonUnrenderable, n.Reason)
		assert.Empty(t, n.ExpectedPath, "no path, so no rename")
	})
	t.Run("a proposal that cannot be rendered on is no name", func(t *testing.T) {
		n := render(map[string]string{"a": "b"}, "a")
		assert.Equal(t, catalogv1alpha1.NamingReasonUnrenderable, n.Reason)
	})
}
