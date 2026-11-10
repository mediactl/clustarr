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

package rename

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func objMeta(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "media", Name: name} }

// TestRootFolderRefResolvesEveryKind is the actuator's root resolution
// table (2026-11-10 design D7, §5.5): a Movie's own root, an Episode's
// Series', an Album's Artist's, a Book's own override else its Author's, an
// Audiobook's own and an Issue's Comic's; "" for an item that is gone, a
// standalone Book with no root, and a kind the loop does not name.
func TestRootFolderRefResolvesEveryKind(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.Movie{ObjectMeta: objMeta("heat"), Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, RootFolderRef: "movies"}},
		&catalogv1alpha1.Series{ObjectMeta: objMeta("firefly"), Spec: catalogv1alpha1.SeriesSpec{TvdbID: 78874, RootFolderRef: "tv"}},
		&catalogv1alpha1.Episode{ObjectMeta: objMeta("firefly-s01e01"), Spec: catalogv1alpha1.EpisodeSpec{SeriesRef: "firefly", SeasonNumber: 1, EpisodeNumber: 1}},
		&catalogv1alpha1.Artist{ObjectMeta: objMeta("boc"), Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb", RootFolderRef: "music"}},
		&catalogv1alpha1.Album{ObjectMeta: objMeta("mhtrtc"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "boc", ReleaseGroupID: "rg"}},
		&catalogv1alpha1.Album{ObjectMeta: objMeta("orphan-album"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "gone", ReleaseGroupID: "rg2"}},
		&catalogv1alpha1.Author{ObjectMeta: objMeta("pratchett"), Spec: catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL1A", RootFolderRef: "books"}},
		&catalogv1alpha1.Book{ObjectMeta: objMeta("guards"), Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett"), WorkID: "w1"}},
		&catalogv1alpha1.Book{ObjectMeta: objMeta("guards-elsewhere"), Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett"), WorkID: "w2", RootFolderRef: ptr.To("books2")}},
		&catalogv1alpha1.Book{ObjectMeta: objMeta("dune"), Spec: catalogv1alpha1.BookSpec{WorkID: "w3", RootFolderRef: ptr.To("books2")}},
		&catalogv1alpha1.Book{ObjectMeta: objMeta("rootless"), Spec: catalogv1alpha1.BookSpec{WorkID: "w4"}},
		&catalogv1alpha1.Audiobook{ObjectMeta: objMeta("guards-audio"), Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "audiobooks"}},
		&catalogv1alpha1.Comic{ObjectMeta: objMeta("saga"), Spec: catalogv1alpha1.ComicSpec{Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "1", RootFolderRef: "comics"}},
		&catalogv1alpha1.Issue{ObjectMeta: objMeta("saga-001"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "001"}},
		&catalogv1alpha1.Issue{ObjectMeta: objMeta("orphan-issue"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "gone", Number: "001"}},
	).Build()
	a := New(Options{})
	for _, tc := range []struct {
		name string
		ref  commonv1.MediaRef
		want string
	}{
		{"a movie's own", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, "movies"},
		{"an episode's series'", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "firefly-s01e01"}, "tv"},
		{"an album's artist's", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "rec"}, "music"},
		{"an album whose artist is gone", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "orphan-album"}, ""},
		{"an author's book", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "guards"}, "books"},
		{"an author's book overriding its root", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "guards-elsewhere"}, "books2"},
		{"a standalone book", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dune"}, "books2"},
		{"a standalone book without a root", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "rootless"}, ""},
		{"an audiobook's own", commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"}, "audiobooks"},
		{"an issue's comic's", commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001"}, "comics"},
		{"an issue whose comic is gone", commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "orphan-issue"}, ""},
		{"an item that is gone", commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "nope"}, ""},
		{"a kind the loop does not name", commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "boc"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mf := &catalogv1alpha1.MediaFile{ObjectMeta: objMeta("f"), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: tc.ref, Path: "/data/x"}}
			got, err := a.rootFolderRef(t.Context(), c, mf)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestRenameModeReadsRenameFilesForEveryKindAndRenameTranscodedForVideoOnly:
// renameFiles on a music root renames an album's track; renameTranscoded
// alone renames nothing but a video file the transcode controller
// transcoded, whatever spec.original says of a non-video file.
func TestRenameModeReadsRenameFilesForEveryKindAndRenameTranscodedForVideoOnly(t *testing.T) {
	roots := []client.Object{
		&catalogv1alpha1.RootFolder{ObjectMeta: objMeta("music"), Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/music", Kind: catalogv1alpha1.RootFolderKindMusic, Naming: catalogv1alpha1.NamingSpec{RenameFiles: ptr.To(true)},
		}},
		&catalogv1alpha1.RootFolder{ObjectMeta: objMeta("audiobooks"), Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/audiobooks", Kind: catalogv1alpha1.RootFolderKindAudiobook, Naming: catalogv1alpha1.NamingSpec{RenameTranscoded: true},
		}},
		&catalogv1alpha1.RootFolder{ObjectMeta: objMeta("movies"), Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/movies", Kind: catalogv1alpha1.RootFolderKindMovie, Naming: catalogv1alpha1.NamingSpec{RenameTranscoded: true},
		}},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(append(roots,
		&catalogv1alpha1.Artist{ObjectMeta: objMeta("boc"), Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb", RootFolderRef: "music"}},
		&catalogv1alpha1.Album{ObjectMeta: objMeta("mhtrtc"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "boc", ReleaseGroupID: "rg"}},
		&catalogv1alpha1.Audiobook{ObjectMeta: objMeta("ab"), Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "audiobooks"}},
		&catalogv1alpha1.Movie{ObjectMeta: objMeta("heat"), Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, RootFolderRef: "movies"}},
	)...).Build()
	a := New(Options{})
	file := func(ref commonv1.MediaRef, original bool) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: objMeta("f"), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: "/data/x", Original: ptr.To(original)}}
	}

	_, mode, err := a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "r"}, true))
	require.NoError(t, err)
	assert.Equal(t, renameWhole, mode, "renameFiles is read for a music root")

	_, mode, err = a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "ab"}, false))
	require.NoError(t, err)
	assert.Equal(t, renameOff, mode, "renameTranscoded is video-only, whatever spec.original says of an audiobook")

	_, mode, err = a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, false))
	require.NoError(t, err)
	assert.Equal(t, renameInFolder, mode, "a transcoded movie file under renameTranscoded")

	_, mode, err = a.renameMode(t.Context(), c, file(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, true))
	require.NoError(t, err)
	assert.Equal(t, renameOff, mode, "an original movie file under renameTranscoded alone")
}

// TestAVacatedCollisionTargetIsRetriedAfterTheRecheckInterval is 2026-11-10
// design D11 at the actuator: a collision is remembered with its time, so
// the same proposal is not tried again within collisionRecheck -- a pass
// that changes nothing raises no second Warning -- and is once it has
// passed, when the file in the way may have moved on (an audiobook's tied
// sibling renamed to "(01)" and then, by its own pass, on to "(02)");
// until then the proposal was never retried before a restart. The
// collision asks for no pass of its own; a refused pass within the
// interval asks for the one that retries it. The interval is the test's
// own (10 s over a fake clock, the production minute asserted once) and
// the advances are literal, so a longer or never-expiring interval and the
// old no-expiry semantics fail it alike.
func TestAVacatedCollisionTargetIsRetriedAfterTheRecheckInterval(t *testing.T) {
	require.Equal(t, time.Minute, collisionRecheck, "the production interval (design D11: retried after a minute)")
	prev := collisionRecheck
	collisionRecheck = 10 * time.Second
	t.Cleanup(func() { collisionRecheck = prev })

	dir := t.TempDir()
	from, to := filepath.Join(dir, "Guards! Guards! (02).mp3"), filepath.Join(dir, "Guards! Guards! (01).mp3")
	require.NoError(t, os.WriteFile(from, []byte("part one"), 0o644))
	require.NoError(t, os.WriteFile(to, []byte("in the way"), 0o644))
	info, err := os.Stat(from)
	require.NoError(t, err)

	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "guards-2", UID: "uid-guards-2"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"},
			Path:     from, SizeBytes: info.Size(), ModTime: metav1.NewTime(info.ModTime()),
		},
		Status: catalogv1alpha1.MediaFileStatus{
			Naming: &catalogv1alpha1.NamingStatus{ExpectedPath: to},
			Conditions: []metav1.Condition{
				{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse, Reason: "Stale"},
				{Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionTrue, Reason: "Ready"},
				{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.RootFolder{ObjectMeta: objMeta("audiobooks"), Spec: catalogv1alpha1.RootFolderSpec{
			Path: dir, Kind: catalogv1alpha1.RootFolderKindAudiobook, Naming: catalogv1alpha1.NamingSpec{RenameFiles: ptr.To(true)},
		}},
		&catalogv1alpha1.Audiobook{ObjectMeta: objMeta("guards-audio"), Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "audiobooks"}},
		mf,
	).Build()
	rec := k8sevents.NewFakeRecorder(16)
	a := New(Options{Recorder: rec})
	clock := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return clock }
	env := &remediation.Env{Reader: c, APIReader: c}

	wake, err := a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Zero(t, wake, "a collision asks for no pass of its own (a permanent one would pass every minute for good)")
	assert.Len(t, withPrefix(drain(rec), "Warning Collision"), 1)
	assert.FileExists(t, from)

	require.NoError(t, os.Remove(to))
	clock = clock.Add(5 * time.Second)
	wake, err = a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Empty(t, drain(rec), "the same proposal is not tried again within the interval")
	assert.Equal(t, 5*time.Second, wake, "and the pass at the interval's end, which will try it, is asked for")
	assert.FileExists(t, from)

	clock = clock.Add(5 * time.Second)
	wake, err = a.Act(t.Context(), env, c, mf)
	require.NoError(t, err)
	assert.Zero(t, wake)
	assert.Len(t, withPrefix(drain(rec), "Normal Renamed"), 1, "the vacated target is taken once the interval has passed")
	assert.NoFileExists(t, from)
	assert.FileExists(t, to)
	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(mf), &got))
	assert.Equal(t, to, got.Spec.Path)
}

// drain is every event rec holds.
func drain(rec *k8sevents.FakeRecorder) []string {
	var out []string
	for {
		select {
		case ev := <-rec.Events:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func withPrefix(evs []string, prefix string) []string {
	var out []string
	for _, ev := range evs {
		if strings.HasPrefix(ev, prefix) {
			out = append(out, ev)
		}
	}
	return out
}
