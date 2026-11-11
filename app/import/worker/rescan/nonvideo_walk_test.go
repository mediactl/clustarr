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

package rescan_test

import (
	"context"
	"maps"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// Walk-level tests of the non-video rules the review of task W7.12 asked
// for: each runs the real walk (Worker.Handle) over a temp root, a fake
// client holding the catalog, fake metadata calls and a fake prober.

// twoDaysAgo is a creation time past the walk's one-day wait for a child.
var twoDaysAgo = metav1.NewTime(time.Now().Add(-48 * time.Hour))

func artistObj(name, mbid, display string, mutate ...func(*catalogv1alpha1.Artist)) *catalogv1alpha1.Artist {
	ar := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Namespace: "library", Name: name},
		Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: mbid, QualityProfileRef: "lossless", RootFolderRef: "lib"},
		Status:     catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{Name: display}},
	}
	for _, m := range mutate {
		m(ar)
	}
	return ar
}

// albumObj is an Album of artist with title ("" for one whose document has
// not arrived) and one track per recording id, numbered from 1.
func albumObj(name, artist, group, title string, recordings ...string) *catalogv1alpha1.Album {
	al := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Namespace: "library", Name: name},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: artist, ReleaseGroupID: group},
	}
	if title != "" {
		al.Status.Metadata = &catalogv1alpha1.AlbumMetadata{Title: title}
	}
	for i, rec := range recordings {
		al.Status.Tracks = append(al.Status.Tracks, catalogv1alpha1.Track{
			RecordingID: rec, Medium: 1, Number: int32(i + 1), Title: rec, //nolint:gosec // a handful of tracks
		})
	}
	return al
}

// probeTags makes the fixture's prober answer each file's tags by its path
// relative to the root (none for a file not listed).
func (f *walkFixture) probeTags(tags map[string]*commonv1.MediaTags) {
	f.w.ProbeAudio = func(_ context.Context, path string) (mediainfo.AudioProbe, error) {
		rel, _ := filepath.Rel(f.root, path)
		return mediainfo.AudioProbe{Codec: "flac", SampleBits: 16, Tags: tags[rel]}, nil
	}
}

// unmatchedFiles is a checkpoint's unmatched file entries (not folders), by
// path.
func unmatchedFiles(p scanprogress.Progress) map[string]scanprogress.UnmatchedFile {
	out := map[string]scanprogress.UnmatchedFile{}
	for _, u := range p.Unmatched {
		if !u.Folder {
			out[u.Path] = u
		}
	}
	return out
}

// An artist folder the catalog knows only by its folder -- a name
// CleanTitle empties ("宇多田ヒカル", "!!!"), or a token whose name part is
// not the artist's -- takes its albums: the walk resolved the folder to the
// Artist, and MatchAlbum reads that resolution off the segment as it is,
// never by re-deciding the artist from an ASCII-only name (review finding:
// matched=0 and "the 0 albums of that artist" on every scan).
func TestAnArtistFolderNamedOnlyByItsFolderTakesItsAlbums(t *testing.T) {
	kurt := "Kurt [mbid-" + nirvanaID + "]"
	f := newWalkFixture(t, catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootDefaults{QualityProfileRef: "lossless"},
		artistObj("utada", "utada-mbid", "Hikaru Utada", func(a *catalogv1alpha1.Artist) { a.Spec.Folder = new("宇多田ヒカル") }),
		albumObj("first-love", "utada", "first-love-group", "First Love", "automatic"),
		artistObj("chk-chk-chk", "chk-mbid", "!!!", func(a *catalogv1alpha1.Artist) { a.Spec.Folder = new("!!!") }),
		albumObj("myth-takes", "chk-chk-chk", "myth-takes-group", "Myth Takes", "myth-takes-1"),
		artistObj("nirvana", nirvanaID, "Nirvana"),
		albumObj("nevermind", "nirvana", "nevermind-group", "Nevermind", "teen-spirit"),
	)
	utada := filepath.Join("宇多田ヒカル", "01 - Automatic.flac")
	bang := filepath.Join("!!!", "01.flac")
	nevermind := filepath.Join(kurt, "01.flac")
	f.plant(t, utada, bang, nevermind)
	f.probeTags(map[string]*commonv1.MediaTags{
		utada:     {MusicBrainzReleaseGroupID: "first-love-group", Album: "First Love", TrackNumber: 1},
		bang:      {Album: "Myth Takes", TrackNumber: 1},
		nevermind: {MusicBrainzReleaseGroupID: "nevermind-group", TrackNumber: 1},
	})

	got, _ := f.handle(t, false)
	assert.Empty(t, got.Unmatched)
	assert.Empty(t, got.AwaitingChildren)
	assert.Empty(t, f.gw.searches, "every folder is an existing artist's")
	obs := f.bus.observed(t, f.root)
	for rel, want := range map[string][2]string{
		utada:     {"first-love", "automatic"},
		bang:      {"myth-takes", "myth-takes-1"},
		nevermind: {"nevermind", "teen-spirit"},
	} {
		o, ok := obs[rel]
		require.True(t, ok, "%s was not attributed", rel)
		require.NotNil(t, o.Attribution, rel)
		assert.Equal(t, "album", o.Attribution.Kind, rel)
		assert.Equal(t, want[0], o.Attribution.Name, rel)
		assert.Equal(t, want[1], o.Attribution.Track, rel)
	}
}

// Every track takes one file (2026-11-10 design D6): a second copy of a
// track the walk attributed, or a file for a track a MediaFile already
// records, is listed for a person, naming the holder, never a second record;
// a record whose file is gone from disk holds nothing (review finding: two
// MediaFiles for one track).
func TestEveryTrackTakesOneFile(t *testing.T) {
	const ns = "library"
	held := filepath.Join("Radiohead", "Zz Rips", "02.flac")
	gone := filepath.Join("Radiohead", "Gone", "03.flac")
	f := newWalkFixture(t, catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootDefaults{QualityProfileRef: "lossless"},
		artistObj("radiohead", radioheadID, "Radiohead"),
		albumObj("ok-computer", "radiohead", "okc-group", "OK Computer", "airbag", "paranoid-android", "subterranean"),
	)
	record := func(name, rel, track string) client.Object {
		return &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec: catalogv1alpha1.MediaFileSpec{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "ok-computer", Track: track},
				Path:     filepath.Join(f.root, rel),
			},
		}
	}
	require.NoError(t, f.w.Client.Create(context.Background(), record("okc-paranoid", held, "paranoid-android")))
	require.NoError(t, f.w.Client.Create(context.Background(), record("okc-gone", gone, "subterranean")))

	flac := filepath.Join("Radiohead", "OK Computer (1997) [FLAC]", "01 - Airbag.flac")
	mp3 := filepath.Join("Radiohead", "OK Computer (1997) [MP3]", "01 - Airbag.mp3")
	rip := filepath.Join("Radiohead", "A Rip", "02 - Paranoid Android.flac")
	moved := filepath.Join("Radiohead", "OK Computer (1997) [FLAC]", "03 - Subterranean Homesick Alien.flac")
	f.plant(t, flac, mp3, rip, moved, held)
	group := func(track int32) *commonv1.MediaTags {
		return &commonv1.MediaTags{MusicBrainzReleaseGroupID: "okc-group", TrackNumber: track}
	}
	f.probeTags(map[string]*commonv1.MediaTags{flac: group(1), mp3: group(1), rip: group(2), moved: group(3)})

	got, _ := f.handle(t, false)
	obs := f.bus.observed(t, f.root)
	assert.ElementsMatch(t, []string{flac, moved, held}, slices.Collect(maps.Keys(obs)),
		"one file per track: the first copy, the recorded file and the track whose record's file is gone")
	assert.Equal(t, "airbag", obs[flac].Attribution.Track)
	assert.Equal(t, "subterranean", obs[moved].Attribution.Track, "a record whose file is gone holds nothing")

	files := unmatchedFiles(got)
	require.Len(t, files, 2, "%v", got.Unmatched)
	assert.Contains(t, files[mp3].Reason, "the file is track airbag of album ok-computer, which "+flac+" already holds")
	assert.Equal(t, []string{"ok-computer"}, files[mp3].Candidates)
	assert.Contains(t, files[rip].Reason, "track paranoid-android of album ok-computer, which "+held+" already holds",
		"the recorded file holds its track though the walk reaches it later")
}

// A file whose container's list has settled without its item, or has not
// come in the day since its item was created, is listed with the reason,
// never counted as awaiting for good where no one sees it (review finding):
// an artist whose fan-out listed no album, one whose listing never came, an
// album whose listing holds no accepted release, and an album whose
// document never came. A file whose list is still to come awaits it; an
// untitled album does not hold a file whose tags name another release group.
func TestAFileWhoseListHasSettledIsListedNotAwaited(t *testing.T) {
	synced := func(cond string, status metav1.ConditionStatus, reason, message string) func(*catalogv1alpha1.Artist) {
		return func(a *catalogv1alpha1.Artist) {
			a.Status.Conditions = append(a.Status.Conditions, metav1.Condition{
				Type: cond, Status: status, Reason: reason, Message: message, LastTransitionTime: twoDaysAgo,
			})
		}
	}
	old := func(a *catalogv1alpha1.Artist) { a.CreationTimestamp = twoDaysAgo }
	bootleg := albumObj("live-bootleg", "bootlegger", "bootleg-group", "Live Bootleg")
	bootleg.Status.Conditions = []metav1.Condition{{
		Type: "TracksSynced", Status: metav1.ConditionFalse, Reason: "NoAcceptedRelease", LastTransitionTime: twoDaysAgo,
		Message: "no release of this group has tracks and a status the artist's metadata profile accepts (releaseStatuses [official])",
	}}
	lost := albumObj("lost", "old-untitled", "lost-group", "")
	lost.CreationTimestamp = twoDaysAgo
	f := newWalkFixture(t, catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootDefaults{QualityProfileRef: "lossless"},
		artistObj("singles", "singles-mbid", "Singles Only",
			synced(catalogv1alpha1.ArtistConditionAlbumsSynced, metav1.ConditionTrue, "Reconciled", "albums synced")),
		artistObj("stalled", "stalled-mbid", "Stalled Band", old,
			synced(catalogv1alpha1.ArtistConditionAlbumsSynced, metav1.ConditionFalse, "RPCError", "album listing RPC failed")),
		artistObj("fresh", "fresh-mbid", "Fresh Band"),
		artistObj("bootlegger", "bootlegger-mbid", "Bootlegger"),
		bootleg,
		albumObj("pending", "bootlegger", "pending-group", "Pending Album"),
		artistObj("old-untitled", "old-mbid", "Old Untitled"),
		albumObj("known", "old-untitled", "known-group", "Known", "k1"),
		lost,
		artistObj("young-untitled", "young-mbid", "Young Untitled"),
		albumObj("known-two", "young-untitled", "known-two-group", "Known Two", "k2"),
		albumObj("coming", "young-untitled", "coming-group", ""),
	)
	var (
		singles    = filepath.Join("Singles Only", "01.flac")
		stalled    = filepath.Join("Stalled Band", "01.flac")
		fresh      = filepath.Join("Fresh Band", "01.flac")
		bootlegged = filepath.Join("Bootlegger", "01.flac")
		pending    = filepath.Join("Bootlegger", "02.flac")
		oldFile    = filepath.Join("Old Untitled", "01.flac")
		otherGroup = filepath.Join("Young Untitled", "01.flac")
		mayBe      = filepath.Join("Young Untitled", "02.flac")
	)
	f.plant(t, singles, stalled, fresh, bootlegged, pending, oldFile, otherGroup, mayBe)
	f.probeTags(map[string]*commonv1.MediaTags{
		bootlegged: {MusicBrainzReleaseGroupID: "bootleg-group", TrackNumber: 1},
		pending:    {MusicBrainzReleaseGroupID: "pending-group", TrackNumber: 1},
		oldFile:    {Album: "Something Else"},
		otherGroup: {MusicBrainzReleaseGroupID: "other-group", Album: "Something Else"},
		mayBe:      {Album: "Something Else"},
	})

	got, _ := f.handle(t, false)
	assert.Equal(t, map[string]int64{"artist": 3}, got.AwaitingChildren,
		"the fresh artist's file, the pending album's and the one an untitled album may still claim")
	files := unmatchedFiles(got)
	assert.ElementsMatch(t, []string{singles, stalled, bootlegged, oldFile, otherGroup}, slices.Collect(maps.Keys(files)))
	assert.Contains(t, files[singles].Reason, "artist singles lists no album under this root folder")
	assert.Contains(t, files[stalled].Reason, "artist stalled has listed no album in the day since it was created")
	assert.Contains(t, files[bootlegged].Reason, "album live-bootleg lists no track the file could be: no release of this group")
	assert.Contains(t, files[oldFile].Reason, `no album of Old Untitled is titled "Something Else"`)
	assert.Contains(t, files[otherGroup].Reason, `no album of Young Untitled is titled "Something Else"`)
	assert.Empty(t, f.bus.observed(t, f.root))
}

// The book root's twin: an author whose fan-out listed no book has none to
// come, and one whose books' documents never came in a day holds no file.
func TestABookFileWhoseAuthorHasSettledIsListed(t *testing.T) {
	author := func(name, display string, mutate ...func(*catalogv1alpha1.Author)) *catalogv1alpha1.Author {
		au := &catalogv1alpha1.Author{
			ObjectMeta: metav1.ObjectMeta{Namespace: "library", Name: name},
			Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: name + "-olid", QualityProfileRef: "books", RootFolderRef: "lib"},
			Status:     catalogv1alpha1.AuthorStatus{Metadata: &catalogv1alpha1.AuthorMetadata{Name: display}},
		}
		for _, m := range mutate {
			m(au)
		}
		return au
	}
	book := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Namespace: "library", Name: "lost-book", CreationTimestamp: twoDaysAgo},
		Spec:       catalogv1alpha1.BookSpec{WorkID: "OL9W", AuthorRef: new("old-author")},
	}
	f := newWalkFixture(t, catalogv1alpha1.RootFolderKindBook, catalogv1alpha1.RootDefaults{QualityProfileRef: "books"},
		author("listed-author", "Listed Author", func(a *catalogv1alpha1.Author) {
			a.Status.Conditions = []metav1.Condition{{
				Type: catalogv1alpha1.AuthorConditionBooksSynced, Status: metav1.ConditionTrue, Reason: "Reconciled",
				LastTransitionTime: twoDaysAgo,
			}}
		}),
		author("fresh-author", "Fresh Author"),
		author("old-author", "Old Author"),
		book,
	)
	listed := filepath.Join("Listed Author", "Some Book.epub")
	fresh := filepath.Join("Fresh Author", "Some Book.epub")
	stale := filepath.Join("Old Author", "Some Book.epub")
	f.plant(t, listed, fresh, stale)

	got, _ := f.handle(t, false)
	assert.Equal(t, map[string]int64{"author": 1}, got.AwaitingChildren, "only the fresh author's file awaits its books")
	assert.ElementsMatch(t, []string{listed, stale}, slices.Collect(maps.Keys(unmatchedFiles(got))))
}

// An artist folder whose files' tags agree on an MBID MusicBrainz has since
// merged is the artist it became: the lookup's answer names the current id,
// which is the identity -- an existing Artist under it takes the folder,
// and no second Artist is proposed under the stale id (review finding).
func TestAMergedArtistIDIsTheArtistItBecame(t *testing.T) {
	const staleID = "0b1d5c4b-8a5c-4bd5-a3a5-6c7c3b8a1e2f"
	f := newWalkFixture(t, catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootDefaults{QualityProfileRef: "lossless"},
		artistObj("nirvana", nirvanaID, "Nirvana"),
	)
	folder := "Nirvana (Sub Pop Singles)"
	track := filepath.Join(folder, "01.flac")
	f.plant(t, track)
	f.probeTags(map[string]*commonv1.MediaTags{track: {AlbumArtist: folder, MusicBrainzArtistID: staleID}})
	f.gw.merged[staleID] = nirvanaID
	f.gw.names[nirvanaID] = "Nirvana"

	got, _ := f.handle(t, false)
	assert.Equal(t, []map[string]string{{metadata.KeyMBArtist: staleID}}, f.gw.lookups)
	assert.Empty(t, f.bus.observed(t, f.root), "the folder is the existing Artist's: nothing is proposed")
	assert.Empty(t, got.Unmatched)
	assert.Equal(t, map[string]int64{"artist": 1}, got.AwaitingChildren, "the file awaits the existing artist's albums")
}

// An audiobook whose ASIN only the file's name carries (Libation's "Title
// [B0...].m4b") is proposed under that ASIN, never another Audible's title
// search answers, and asks Audible nothing; a file of the folder naming
// another ASIN is listed, not attached to the first (review finding).
func TestAnAudiobookFileNamingItsASINIsThatAudiobook(t *testing.T) {
	f := newWalkFixture(t, catalogv1alpha1.RootFolderKindAudiobook, catalogv1alpha1.RootDefaults{QualityProfileRef: "spoken"})
	folder := filepath.Join("Brandon Sanderson", "The Way of Kings")
	own := filepath.Join(folder, "The Way of Kings [B003ZWFO7E].m4b")
	other := filepath.Join(folder, "The Way of Kings [B0OTHER000].m4b")
	f.plant(t, own, other)
	f.w.ProbeAudio = func(context.Context, string) (mediainfo.AudioProbe, error) {
		return mediainfo.AudioProbe{Codec: "aac"}, nil
	}
	f.gw.hits["The Way of Kings"] = []metadata.SearchHit{{
		IDs: metadata.ExternalIDs{metadata.KeyASIN: "B00EDITION"}, Title: "The Way of Kings",
		Authors: []metadata.NamedRef{{Name: "Brandon Sanderson"}},
	}}

	got, _ := f.handle(t, false)
	assert.Empty(t, f.gw.searches, "the file's own ASIN decides; Audible is not asked")
	obs := f.bus.observed(t, f.root)
	require.Contains(t, obs, own)
	require.NotNil(t, obs[own].Attribution)
	require.NotNil(t, obs[own].Attribution.Create)
	assert.Equal(t, "B003ZWFO7E", obs[own].Attribution.Create.ASIN)
	assert.Equal(t, "The Way of Kings", obs[own].Attribution.Create.Title)
	assert.Equal(t, folder, obs[own].Attribution.Create.Folder)
	assert.NotContains(t, obs, other)
	assert.Contains(t, unmatchedFiles(got)[other].Reason, "the path carries ASIN B0OTHER000")
}

// MusicBrainz's special-purpose artists are never proposed, whichever layer
// names one: a compilations folder the search answers Various Artists for
// exactly (whatever band of that name it also lists), a folder whose token
// names it, and one whose files' tags agree on it -- each listed once, as a
// folder (review finding: the Various Artists folder became a monitored
// Artist browsing 2,000 release groups).
func TestASpecialPurposeArtistIsNeverProposed(t *testing.T) {
	const various = "89ad4ac3-39f7-470e-963a-56509c546377"
	f := newWalkFixture(t, catalogv1alpha1.RootFolderKindMusic, catalogv1alpha1.RootDefaults{QualityProfileRef: "lossless"})
	token := "Compilations [mbid-" + various + "]"
	f.plant(t,
		filepath.Join("Various Artists", "01.flac"),
		filepath.Join(token, "01.flac"),
		filepath.Join("VA Rips", "01.flac"),
		filepath.Join("VA Rips", "02.flac"),
	)
	vaTags := &commonv1.MediaTags{AlbumArtist: "VA Rips", MusicBrainzArtistID: various}
	f.probeTags(map[string]*commonv1.MediaTags{
		filepath.Join("VA Rips", "01.flac"): vaTags,
		filepath.Join("VA Rips", "02.flac"): vaTags,
	})
	f.gw.hits["Various Artists"] = []metadata.SearchHit{
		hit("Various Artists", "add compilations to this artist", various, 100),
		hit("Various Artists", "a band", "va-band", 90),
	}

	got, _ := f.handle(t, false)
	assert.Empty(t, f.bus.observed(t, f.root), "nothing is proposed")
	assert.Empty(t, f.gw.lookups, "a special-purpose id is refused before the gateway is asked to validate it")
	assert.Equal(t, []string{"Various Artists"}, f.gw.searches)
	folders := folderEntries(got)
	require.Len(t, folders, 3, "%v", got.Unmatched)
	for _, folder := range []string{"Various Artists", token, "VA Rips"} {
		assert.Contains(t, folders[folder].Reason, "special-purpose artist Various Artists ("+various, folder)
	}
	assert.Equal(t, map[string]int64{"artist": 4}, got.AwaitingChildren)
}
