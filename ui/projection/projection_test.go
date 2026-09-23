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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	subtitlev1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui/projection"
)

// testScheme registers every kind buildRelatedIndex and listItems list:
// corev1 is not needed here, unlike ui.NewReaderScheme, since projection
// never lists Pods, Nodes or anything else outside the five Clustarr API
// groups it actually reads.
func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(s))
	require.NoError(t, downloadv1.AddToScheme(s))
	require.NoError(t, transcodev1.AddToScheme(s))
	require.NoError(t, subtitlev1.AddToScheme(s))
	return s
}

// countingReader wraps a client.Reader and counts every List call, so a
// test can prove a shared projection computes once per tick no matter how
// many subscribers are listening -- Subscribe must never trigger a List of
// its own.
type countingReader struct {
	client.Reader
	calls *atomic.Int64
}

func (r *countingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.calls.Add(1)
	return r.Reader.List(ctx, list, opts...)
}

// listCallsPerTick is buildRelatedIndex's five List calls (MediaFile,
// Download, Search, TranscodeJob, SubtitleRequest) plus listItems' ten
// (Movie, Series, Episode, Album, Artist, Author, Book, Audiobook, Comic,
// Issue) -- pkg/pipeline/project.go's own describeItem type-switch names
// exactly those ten catalog kinds -- plus one more: project's own
// listLibraryScans call (Task G3-3), the one new List the Unmatched stream
// adds to the shared round rather than running a ticker of its own.
const listCallsPerTick = 5 + 10 + 1

// ownerRef builds a controlling OwnerReference to owner, the same shape
// catalogarr/worker/grab/perform.go's k8s.OwnerReferenceAC produces for a
// real Download -- the one production writer this repo has today that sets
// an owner reference on anything relatedIndex reads.
func ownerRef(owner client.Object, kind string) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: catalogv1.GroupVersion.String(),
		Kind:       kind,
		Name:       owner.GetName(),
		UID:        owner.GetUID(),
		Controller: ptr.To(true),
	}
}

// receiveNonEmpty drains ch until it sees a projection with at least one
// entry, or fails the test after timeout. Subscribe's own doc comment says
// it delivers "the current projection immediately", which can race a
// Projection's own first tick and arrive empty; a real update always
// follows once that tick completes, so looping past an empty frame is the
// correct way to wait for "the projection this test set up", not a retry
// for flakiness's sake.
func receiveNonEmpty(t *testing.T, ch <-chan []pipeline.Entry, timeout time.Duration) []pipeline.Entry {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case entries := <-ch:
			if len(entries) > 0 {
				return entries
			}
		case <-deadline:
			t.Fatal("timed out waiting for a non-empty projection")
			return nil
		}
	}
}

// receiveNonEmptyDownloads is [receiveNonEmpty]'s Task D3-3 counterpart for
// SubscribeDownloads' channel, for the identical reason: Subscribe(Downloads)
// delivers the current snapshot immediately, which can race the Projection's
// own first tick and arrive empty.
func receiveNonEmptyDownloads(t *testing.T, ch <-chan []downloadv1.Download, timeout time.Duration) []downloadv1.Download {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case downloads := <-ch:
			if len(downloads) > 0 {
				return downloads
			}
		case <-deadline:
			t.Fatal("timed out waiting for a non-empty downloads projection")
			return nil
		}
	}
}

// TestSubscribersShareOneListRound is Task D3-1's central assertion (design
// plan, R4): two subscribers must see the same slice computed from a single
// list round, not one list round each. interval is an hour so exactly one
// tick -- Run's own immediate one -- happens during the test, making
// listCallsPerTick an exact, not just an upper, bound.
func TestSubscribersShareOneListRound(t *testing.T) {
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption", Namespace: "default", UID: "movie-uid"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Title: "The Shawshank Redemption"}},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(movie).Build()

	var calls atomic.Int64
	reader := &countingReader{Reader: fakeClient, calls: &calls}

	proj := projection.New(reader, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	ch1, unsub1 := proj.Subscribe()
	defer unsub1()
	ch2, unsub2 := proj.Subscribe()
	defer unsub2()

	got1 := receiveNonEmpty(t, ch1, 2*time.Second)
	got2 := receiveNonEmpty(t, ch2, 2*time.Second)
	require.Equal(t, got1, got2, "both subscribers must see the identical slice from the same list round")
	require.Len(t, got1, 1)

	// Give any errant extra tick a moment to happen before asserting the
	// call count -- interval is an hour, so nothing should arrive, but a
	// bug that re-lists per-subscriber would show up as > listCallsPerTick
	// here rather than racing this assertion.
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, listCallsPerTick, calls.Load(),
		"two subscribers must not cause more than one list round's worth of List calls")
}

// TestDownloadsSubscribersShareThePipelineListRound is Task D3-3's
// generalisation of TestSubscribersShareOneListRound (ruling R4): a
// SubscribeDownloads subscriber must see the Download list gathered by the
// SAME tick that already feeds Subscribe -- buildRelatedIndex's one List of
// Download, kept for ownership resolution -- not a List call of its own.
// interval is an hour, exactly as above, so listCallsPerTick stays an exact
// bound with both a pipeline and a downloads subscriber attached at once.
func TestDownloadsSubscribersShareThePipelineListRound(t *testing.T) {
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption", Namespace: "default", UID: "movie-uid"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Title: "The Shawshank Redemption"}},
	}
	download := &downloadv1.Download{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "shawshank-download",
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{ownerRef(movie, "Movie")},
		},
		Spec: downloadv1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Target:   commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
		},
		Status: downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseDownloading, ProgressPercent: 10},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(movie, download).Build()

	var calls atomic.Int64
	reader := &countingReader{Reader: fakeClient, calls: &calls}

	proj := projection.New(reader, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	entriesCh, unsubEntries := proj.Subscribe()
	defer unsubEntries()
	downloadsCh, unsubDownloads := proj.SubscribeDownloads()
	defer unsubDownloads()

	gotEntries := receiveNonEmpty(t, entriesCh, 2*time.Second)
	require.Len(t, gotEntries, 1, "the pipeline stream must still see the one Movie")

	gotDownloads := receiveNonEmptyDownloads(t, downloadsCh, 2*time.Second)
	require.Len(t, gotDownloads, 1)
	require.Equal(t, "shawshank-download", gotDownloads[0].Name)

	// Give any errant extra tick or extra List a moment to happen before
	// asserting the call count, exactly as TestSubscribersShareOneListRound
	// does: interval is an hour, so nothing further should arrive.
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, listCallsPerTick, calls.Load(),
		"a downloads subscriber must add no List call beyond the one round the pipeline subscriber already shares")
}

// TestUnsubscribeStopsDelivery proves the func Subscribe returns actually
// removes the caller from the broadcast set: after calling it, a fast ticker
// must not deliver anything further to the channel the test still holds.
func TestUnsubscribeStopsDelivery(t *testing.T) {
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption", Namespace: "default", UID: "movie-uid"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Title: "The Shawshank Redemption"}},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(movie).Build()

	proj := projection.New(fakeClient, 15*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	ch, unsubscribe := proj.Subscribe()
	receiveNonEmpty(t, ch, 2*time.Second)

	unsubscribe()

	// Drain whatever might already be buffered from before unsubscribe won
	// the race with the next tick, then confirm nothing further ever
	// arrives across several more tick intervals.
	select {
	case <-ch:
	default:
	}
	time.Sleep(150 * time.Millisecond) // ~10 more ticks at 15ms
	select {
	case v, ok := <-ch:
		t.Fatalf("received a value after unsubscribe (ok=%v, len=%d); unsubscribe did not remove the channel", ok, len(v))
	default:
	}
}

// TestOwnershipIndexingPutsADownloadUnderTheRightMovie is the ownership
// half of the D3-1 brief: a Download owned (via metav1.OwnerReference.UID,
// never by matching spec.target's Name) by Movie A must show up in Movie
// A's pipeline row and must not leak into Movie B's, which has no Download
// at all.
func TestOwnershipIndexingPutsADownloadUnderTheRightMovie(t *testing.T) {
	movieA := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "movie-a", Namespace: "default", UID: "movie-a-uid"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Title: "Movie A"}},
	}
	movieB := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "movie-b", Namespace: "default", UID: "movie-b-uid"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Title: "Movie B"}},
	}
	download := &downloadv1.Download{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "movie-a-download",
			Namespace:       "default",
			OwnerReferences: []metav1.OwnerReference{ownerRef(movieA, "Movie")},
		},
		Spec: downloadv1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Target:   commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movieA.Name},
		},
		Status: downloadv1.DownloadStatus{
			Phase:           downloadv1.DownloadPhaseDownloading,
			ProgressPercent: 50,
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(movieA, movieB, download).Build()

	proj := projection.New(fakeClient, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	var entries []pipeline.Entry
	require.Eventually(t, func() bool {
		entries = proj.Entries(ctx)
		return len(entries) == 2
	}, 2*time.Second, 10*time.Millisecond, "projection never produced both movies")

	byName := map[string]pipeline.Entry{}
	for _, e := range entries {
		byName[e.Ref.Name] = e
	}

	a, ok := byName["movie-a"]
	require.True(t, ok, "movie-a must have a pipeline entry")
	require.Equal(t, pipeline.StageDownloading, a.Stage,
		"movie-a owns the Download and must show its Downloading stage")
	require.EqualValues(t, 50, a.Percent)

	b, ok := byName["movie-b"]
	require.True(t, ok, "movie-b must have a pipeline entry")
	require.NotEqual(t, pipeline.StageDownloading, b.Stage,
		"movie-b owns no Download and must not inherit movie-a's stage")
}

// TestLibraryProjectionDerivesMonitoredPhaseAndHasFilePerKind is Task G3-3's
// own per-kind coverage of describeLibraryItem, exercised the same way every
// other test in this file exercises unexported logic: through the exported
// surface ([Projection.Library]) over a fake client, never by importing an
// internal function directly (mirroring pkg/pipeline/project_test.go's own
// external-package convention).
//
// It covers every catalog kind the shared listItems call lists: the six with
// their own status.phase (Movie, Series, Episode, Album, Book, Audiobook),
// Issue (hasFile but no phase at all), and the three collection parents with
// neither (Artist, Author, Comic) -- whose hasFile instead reads their
// child-file count, per describeLibraryItem's own doc comment.
func TestLibraryProjectionDerivesMonitoredPhaseAndHasFilePerKind(t *testing.T) {
	unmonitored := false

	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "movie-1", Namespace: "default", UID: "movie-1-uid"},
		Spec:       catalogv1.MovieSpec{Monitored: ptr.To(true)},
		Status: catalogv1.MovieStatus{
			Metadata: &catalogv1.MovieMetadata{Title: "Movie One"},
			Phase:    catalogv1.MoviePhase("Wanted"),
			HasFile:  true,
		},
	}
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "series-1", Namespace: "default", UID: "series-1-uid"},
		Spec:       catalogv1.SeriesSpec{Monitored: &unmonitored},
		Status: catalogv1.SeriesStatus{
			Metadata: &catalogv1.SeriesMetadata{Title: "Series One"},
			Phase:    catalogv1.SeriesPhaseUnmonitored,
		},
	}
	episode := &catalogv1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "episode-1", Namespace: "default", UID: "episode-1-uid"},
		Spec:       catalogv1.EpisodeSpec{Monitored: ptr.To(true)},
		Status:     catalogv1.EpisodeStatus{Title: "Episode One", Phase: catalogv1.EpisodePhase("Downloading"), HasFile: true},
	}
	album := &catalogv1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "album-1", Namespace: "default", UID: "album-1-uid"},
		Spec:       catalogv1.AlbumSpec{Monitored: ptr.To(true)},
		Status: catalogv1.AlbumStatus{
			Metadata:       &catalogv1.AlbumMetadata{Title: "Album One"},
			Phase:          catalogv1.AlbumPhaseDownloading,
			TrackFileCount: 3,
		},
	}
	artist := &catalogv1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "artist-1", Namespace: "default", UID: "artist-1-uid"},
		Spec:       catalogv1.ArtistSpec{Monitored: ptr.To(true)},
		Status: catalogv1.ArtistStatus{
			Metadata:       &catalogv1.ArtistMetadata{Name: "Artist One"},
			AlbumFileCount: 2,
		},
	}
	author := &catalogv1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "author-1", Namespace: "default", UID: "author-1-uid"},
		Spec:       catalogv1.AuthorSpec{Monitored: nil},
		Status:     catalogv1.AuthorStatus{Metadata: &catalogv1.AuthorMetadata{Name: "Author One"}, BookFileCount: 0},
	}
	book := &catalogv1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "book-1", Namespace: "default", UID: "book-1-uid"},
		Spec:       catalogv1.BookSpec{Monitored: ptr.To(true)},
		Status: catalogv1.BookStatus{
			Metadata: &catalogv1.BookMetadata{Title: "Book One"},
			Phase:    catalogv1.BookPhaseCutoffUnmet,
			HasFile:  true,
		},
	}
	audiobook := &catalogv1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "audiobook-1", Namespace: "default", UID: "audiobook-1-uid"},
		Spec:       catalogv1.AudiobookSpec{Monitored: &unmonitored},
		Status: catalogv1.AudiobookStatus{
			Metadata: &catalogv1.AudiobookMetadata{Title: "Audiobook One"},
			Phase:    catalogv1.AudiobookPhaseUnmonitored,
			HasFile:  false,
		},
	}
	comic := &catalogv1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "comic-1", Namespace: "default", UID: "comic-1-uid"},
		Spec:       catalogv1.ComicSpec{Monitored: ptr.To(true)},
		Status: catalogv1.ComicStatus{
			Metadata:       &catalogv1.ComicMetadata{Title: "Comic One"},
			IssueFileCount: 1,
		},
	}
	issue := &catalogv1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: "issue-1", Namespace: "default", UID: "issue-1-uid"},
		Spec:       catalogv1.IssueSpec{Monitored: ptr.To(true)},
		Status:     catalogv1.IssueStatus{Title: "Issue One", HasFile: true},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(movie, series, episode, album, artist, author, book, audiobook, comic, issue).Build()

	proj := projection.New(fakeClient, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	var items []projection.LibraryItem
	require.Eventually(t, func() bool {
		items = proj.Library(ctx)
		return len(items) == 10
	}, 2*time.Second, 10*time.Millisecond, "projection never produced all ten library items")

	byName := map[string]projection.LibraryItem{}
	for _, it := range items {
		byName[it.Ref.Name] = it
	}

	m := byName["movie-1"]
	require.Equal(t, commonv1.MediaKindMovie, m.Kind)
	require.Equal(t, "Movie One", m.Title)
	require.True(t, m.Monitored)
	require.Equal(t, "Wanted", m.Phase)
	require.True(t, m.HasFile)

	s := byName["series-1"]
	require.False(t, s.Monitored, "series-1 has spec.monitored=false")
	require.Equal(t, string(catalogv1.SeriesPhaseUnmonitored), s.Phase)
	require.False(t, s.HasFile, "Series has no hasFile concept of its own")

	e := byName["episode-1"]
	require.True(t, e.Monitored)
	require.Equal(t, "Downloading", e.Phase)
	require.True(t, e.HasFile)

	al := byName["album-1"]
	require.Equal(t, string(catalogv1.AlbumPhaseDownloading), al.Phase)
	require.True(t, al.HasFile, "trackFileCount > 0 must read as hasFile")

	ar := byName["artist-1"]
	require.Empty(t, ar.Phase, "Artist is a collection parent with no phase concept")
	require.True(t, ar.HasFile, "albumFileCount > 0 must read as hasFile for a collection parent")

	au := byName["author-1"]
	require.True(t, au.Monitored, "a nil spec.monitored must default to true")
	require.Empty(t, au.Phase)
	require.False(t, au.HasFile, "bookFileCount == 0 must read as no file")

	bk := byName["book-1"]
	require.Equal(t, string(catalogv1.BookPhaseCutoffUnmet), bk.Phase)
	require.True(t, bk.HasFile)

	ab := byName["audiobook-1"]
	require.False(t, ab.Monitored)
	require.Equal(t, string(catalogv1.AudiobookPhaseUnmonitored), ab.Phase)
	require.False(t, ab.HasFile)

	cm := byName["comic-1"]
	require.Empty(t, cm.Phase, "Comic is a collection parent with no phase concept")
	require.True(t, cm.HasFile, "issueFileCount > 0 must read as hasFile for a collection parent")

	is := byName["issue-1"]
	require.Empty(t, is.Phase, "Issue has no phase field at all")
	require.True(t, is.HasFile)
}

// TestUnmatchedProjectionFlattensAndSortsAcrossScans proves [Projection.
// Unmatched] merges status.unmatched across every current LibraryScan
// (rather than only the most recent one) and re-sorts the merged result
// newest-first by SeenAt, since concatenating separate scans' already
// newest-first lists in List order would not itself be sorted.
func TestUnmatchedProjectionFlattensAndSortsAcrossScans(t *testing.T) {
	older := metav1.NewTime(time.Now().Add(-time.Hour))
	newer := metav1.NewTime(time.Now())

	scanA := &catalogv1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "scan-a", Namespace: "default", UID: "scan-a-uid"},
		Spec:       catalogv1.LibraryScanSpec{RootFolderRef: "movies"},
		Status: catalogv1.LibraryScanStatus{
			Unmatched: []catalogv1.UnmatchedFile{
				{Path: "old-file.mkv", Reason: "no embedded id", SeenAt: older},
			},
		},
	}
	scanB := &catalogv1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "scan-b", Namespace: "default", UID: "scan-b-uid"},
		Spec:       catalogv1.LibraryScanSpec{RootFolderRef: "tv"},
		Status: catalogv1.LibraryScanStatus{
			Unmatched: []catalogv1.UnmatchedFile{
				{Path: "new-file.mkv", Reason: "ambiguous title", Candidates: []string{"movie-a", "movie-b"}, SeenAt: newer},
			},
		},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(scanA, scanB).Build()

	proj := projection.New(fakeClient, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	var entries []projection.UnmatchedEntry
	require.Eventually(t, func() bool {
		entries = proj.Unmatched(ctx)
		return len(entries) == 2
	}, 2*time.Second, 10*time.Millisecond, "projection never produced both unmatched files")

	require.Equal(t, "new-file.mkv", entries[0].Path, "the newer file (scan-b) must sort first")
	require.Equal(t, "tv", entries[0].RootFolder)
	require.Equal(t, types.NamespacedName{Namespace: "default", Name: "scan-b"}, entries[0].ScanRef)
	require.Equal(t, []string{"movie-a", "movie-b"}, entries[0].Candidates)

	require.Equal(t, "old-file.mkv", entries[1].Path)
	require.Equal(t, "movies", entries[1].RootFolder)
	require.Equal(t, "no embedded id", entries[1].Reason)
}

// TestLibraryAndUnmatchedSubscribersShareThePipelineListRound is Task
// G3-3's own generalisation of TestDownloadsSubscribersShareThePipelineListRound
// (ruling R4): a SubscribeLibrary or SubscribeUnmatched subscriber must not
// add a List call of its own beyond the one shared round listCallsPerTick
// already accounts for -- Library reuses listItems' ten List calls, and
// Unmatched adds exactly the one LibraryScan List call folded into
// listCallsPerTick above.
func TestLibraryAndUnmatchedSubscribersShareThePipelineListRound(t *testing.T) {
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "shawshank-redemption", Namespace: "default", UID: "movie-uid"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Title: "The Shawshank Redemption"}},
	}
	scan := &catalogv1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "scan-a", Namespace: "default", UID: "scan-a-uid"},
		Spec:       catalogv1.LibraryScanSpec{RootFolderRef: "movies"},
		Status: catalogv1.LibraryScanStatus{
			Unmatched: []catalogv1.UnmatchedFile{{Path: "x.mkv", Reason: "no id", SeenAt: metav1.Now()}},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(movie, scan).Build()

	var calls atomic.Int64
	reader := &countingReader{Reader: fakeClient, calls: &calls}

	proj := projection.New(reader, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	entriesCh, unsubEntries := proj.Subscribe()
	defer unsubEntries()
	libraryCh, unsubLibrary := proj.SubscribeLibrary()
	defer unsubLibrary()
	unmatchedCh, unsubUnmatched := proj.SubscribeUnmatched()
	defer unsubUnmatched()

	gotEntries := receiveNonEmpty(t, entriesCh, 2*time.Second)
	require.Len(t, gotEntries, 1)

	deadline := time.After(2 * time.Second)
	var gotLibrary []projection.LibraryItem
	for len(gotLibrary) == 0 {
		select {
		case gotLibrary = <-libraryCh:
		case <-deadline:
			t.Fatal("timed out waiting for a non-empty library projection")
		}
	}
	require.Len(t, gotLibrary, 1)
	require.Equal(t, "shawshank-redemption", gotLibrary[0].Ref.Name)

	deadline = time.After(2 * time.Second)
	var gotUnmatched []projection.UnmatchedEntry
	for len(gotUnmatched) == 0 {
		select {
		case gotUnmatched = <-unmatchedCh:
		case <-deadline:
			t.Fatal("timed out waiting for a non-empty unmatched projection")
		}
	}
	require.Len(t, gotUnmatched, 1)
	require.Equal(t, "x.mkv", gotUnmatched[0].Path)

	// Give any errant extra tick or extra List a moment to happen before
	// asserting the call count, exactly as the downloads-stream analogue
	// above does: interval is an hour, so nothing further should arrive.
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, listCallsPerTick, calls.Load(),
		"library and unmatched subscribers must add no List call beyond the one shared round")
}
