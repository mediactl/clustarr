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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/trailers"
	"github.com/mediactl/clustarr/pkg/ytdlp"
)

const (
	ns        = "media"
	moviePlex = "5f40de8fce2564003fabc407"
	showPlex  = "5d9c08ed46115600200ade5e"
	s4Plex    = "5d9c08ed46115600200ade64"
)

var (
	plexTrailerClip = json.RawMessage(`{"type":"clip","subtype":"trailer","extraType":1,"title":"Trailer"}`)
	plexOtherClip   = json.RawMessage(`{"type":"clip","subtype":"behindTheScenes","extraType":5,"title":"Making of"}`)
	official        = trailers.Video{Site: "YouTube", Key: "hnEwJKVWjFM", Name: "Official Trailer", Type: "Trailer", Official: true, Language: "en", Height: 1080, Provider: "tmdb"}
)

// rig is a sweeper over a fake catalog, an in-memory bucket and a media
// volume in a temporary directory, with the gateway, the program and
// FFmpeg replaced by what the test sets.
type rig struct {
	*Sweeper
	t       *testing.T
	clock   *clockwork.FakeClock
	root    string
	extras  map[string][]json.RawMessage
	videos  []trailers.Video
	fetched []string // pages fetched
	fetch   func(dir string) (ytdlp.Fetched, error)
	muxed   int
}

func newRig(t *testing.T, objs ...client.Object) *rig {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(scheme))
	data := t.TempDir()
	root := filepath.Join(data, "media", "movies")
	require.NoError(t, os.MkdirAll(root, 0o755))
	objs = append(objs, &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "library", Namespace: ns},
		Spec:       catalogv1.RootFolderSpec{Path: root},
	})
	bus := membus.New(clockwork.NewRealClock())
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))

	r := &rig{t: t, clock: clockwork.NewFakeClockAt(time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)), root: root, extras: map[string][]json.RawMessage{}}
	r.fetch = func(dir string) (ytdlp.Fetched, error) {
		v, a := filepath.Join(dir, "137.mp4"), filepath.Join(dir, "140.m4a")
		require.NoError(t, os.WriteFile(v, []byte("picture"), 0o644))
		require.NoError(t, os.WriteFile(a, []byte("sound"), 0o644))
		return ytdlp.Fetched{Video: v, Audio: a}, nil
	}
	r.Sweeper = &Sweeper{
		Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(), Namespace: ns,
		KV: bus.KV(events.BucketTrailers),
		PlexExtras: func(_ context.Context, plexID string) ([]json.RawMessage, error) {
			got, ok := r.extras[plexID]
			if !ok {
				return nil, errors.New("the gateway did not answer")
			}
			return got, nil
		},
		Videos: func(context.Context, commonv1.MediaKind, map[string]string) ([]trailers.Video, error) {
			return r.videos, nil
		},
		Fetch: func(_ context.Context, url, dir string) (ytdlp.Fetched, error) {
			r.fetched = append(r.fetched, url)
			return r.fetch(dir)
		},
		Mux: func(_ context.Context, video, audio, output string) error {
			r.muxed++
			return os.WriteFile(output, []byte("picture+sound"), 0o644)
		},
		Probe: func(_ context.Context, path string) (Facts, error) {
			fi, err := os.Stat(path)
			if err != nil {
				return Facts{}, err
			}
			return Facts{SizeBytes: fi.Size(), DurationMillis: 127000, Width: 1920, Height: 1080, VideoCodec: "h264", AudioCodec: "aac"}, nil
		},
		Place:      os.Rename,
		ScratchDir: t.TempDir(), Language: "en", Pace: time.Nanosecond, Clock: r.clock,
	}
	return r
}

// pass runs one pass; the pace after a fetch is on the fake clock, so the
// clock is moved until the pass returns.
func (r *rig) pass() {
	r.t.Helper()
	done := make(chan error, 1)
	go func() { done <- r.Pass(context.Background()) }()
	for {
		select {
		case err := <-done:
			require.NoError(r.t, err)
			return
		default:
			r.clock.Advance(time.Millisecond)
			time.Sleep(time.Millisecond)
		}
	}
}

func (r *rig) entry(kind string, uid types.UID) (trailers.Entry, bool) {
	r.t.Helper()
	e, err := r.KV.Get(context.Background(), trailers.Key(kind, string(uid)))
	if errors.Is(err, events.ErrKeyNotFound) {
		return trailers.Entry{}, false
	}
	require.NoError(r.t, err)
	got, err := trailers.Decode(e.Value)
	require.NoError(r.t, err)
	return got, true
}

func movie(name string, uid types.UID, hasFile bool, ids map[string]string) *catalogv1.Movie {
	m := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: uid},
		Spec:       catalogv1.MovieSpec{RootFolderRef: "library"},
		Status:     catalogv1.MovieStatus{HasFile: hasFile},
	}
	if ids != nil {
		m.Status.Metadata = &catalogv1.MovieMetadata{ExternalIDs: ids}
	}
	return m
}

// An item Plex has no trailer for gets the provider's pick: fetched,
// joined, placed under its root folder where no library walk enters, and
// recorded with what the file holds. The next pass fetches nothing.
func TestAnItemPlexHasNoTrailerForGetsTheProvidersPick(t *testing.T) {
	r := newRig(t, movie("lamb", "uid-lamb", true, map[string]string{"tmdb": "788929", "plex": moviePlex}))
	r.extras[moviePlex] = []json.RawMessage{plexOtherClip}
	r.videos = []trailers.Video{{Site: "YouTube", Key: "teaser00001", Type: "Teaser", Official: true, Language: "en"}, official}

	r.pass()

	e, ok := r.entry(trailers.KindMovie, "uid-lamb")
	require.True(t, ok)
	assert.Equal(t, trailers.StateReady, e.State)
	assert.Equal(t, filepath.Join(r.root, ".clustarr", "trailers", "movie-uid-lamb.mp4"), e.Path)
	assert.Equal(t, official, *e.Video)
	assert.Equal(t, int64(127000), e.DurationMillis)
	assert.Equal(t, 1080, e.Height)
	assert.Equal(t, "h264", e.VideoCodec)
	assert.Equal(t, r.clock.Now().Truncate(time.Hour), e.CheckedAt.Truncate(time.Hour))
	body, err := os.ReadFile(e.Path)
	require.NoError(t, err)
	assert.Equal(t, "picture+sound", string(body))
	assert.Equal(t, []string{"https://www.youtube.com/watch?v=hnEwJKVWjFM"}, r.fetched)
	left, err := os.ReadDir(r.ScratchDir)
	require.NoError(t, err)
	assert.Empty(t, left, "the scratch directory of the fetch is removed")

	r.pass()
	assert.Len(t, r.fetched, 1)
	assert.Equal(t, 1, r.muxed)
}

// What Plex already has, what no provider names, and what is neither
// monitored nor held are each decided without a fetch; an item whose
// metadata has not come, and one the gateway cannot answer for, are left
// for the next pass with no record.
func TestOnlyAnItemThatNeedsATrailerIsFetchedFor(t *testing.T) {
	monitored := true
	wanted := movie("wanted", "uid-wanted", false, map[string]string{"tmdb": "1"})
	wanted.Spec.Monitored = &monitored
	r := newRig(t,
		movie("has-plex-trailer", "uid-plex", true, map[string]string{"tmdb": "2", "plex": moviePlex}),
		movie("unresolved", "uid-unresolved", true, nil),
		movie("unwanted", "uid-unwanted", false, map[string]string{"tmdb": "3"}),
		movie("unanswered", "uid-unanswered", true, map[string]string{"tmdb": "4", "plex": "5d776d1796b655001fe3f324"}),
		wanted,
	)
	r.extras[moviePlex] = []json.RawMessage{plexOtherClip, plexTrailerClip}

	r.pass()

	e, ok := r.entry(trailers.KindMovie, "uid-plex")
	require.True(t, ok)
	assert.Equal(t, trailers.StatePlex, e.State)
	e, ok = r.entry(trailers.KindMovie, "uid-wanted")
	require.True(t, ok, "a monitored item without a file is decided: its trailer is there before its file")
	assert.Equal(t, trailers.StateNone, e.State)
	for _, uid := range []types.UID{"uid-unresolved", "uid-unwanted", "uid-unanswered"} {
		_, ok := r.entry(trailers.KindMovie, uid)
		assert.False(t, ok, uid)
	}
	assert.Empty(t, r.fetched)

	// A decision stands until it lapses: nothing is asked again the next
	// day, and "none" is looked at again two weeks on.
	r.videos = []trailers.Video{official}
	r.clock.Advance(24 * time.Hour)
	r.pass()
	assert.Empty(t, r.fetched)
	r.clock.Advance(trailers.NoneRecheck)
	r.pass()
	assert.Len(t, r.fetched, 1)
}

// A show is asked about by its own Plex id and then its seasons', as the
// provider's extras route answers them: a trailer on any is Plex's.
func TestAShowWithATrailerOnASeasonHasPlexs(t *testing.T) {
	show := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "succession", Namespace: ns, UID: "uid-show"},
		Spec:       catalogv1.SeriesSpec{RootFolderRef: "library"},
		Status: catalogv1.SeriesStatus{EpisodeFileCount: 39, Metadata: &catalogv1.SeriesMetadata{
			ExternalIDs: map[string]string{"tvdb": "338186", "plex": showPlex},
			PlexSeasons: []catalogv1.PlexSeasonRef{{Number: 4, ID: s4Plex}, {Number: 0, ID: "5d9c08ed46115600200ade60"}},
		}},
	}
	r := newRig(t, show)
	r.extras[showPlex] = []json.RawMessage{}
	r.extras[s4Plex] = []json.RawMessage{plexTrailerClip}
	r.videos = []trailers.Video{official}

	r.pass()

	e, ok := r.entry(trailers.KindSeries, "uid-show")
	require.True(t, ok)
	assert.Equal(t, trailers.StatePlex, e.State)
	assert.Empty(t, r.fetched)
}

// A failed fetch is recorded and tried again after its backoff, a page
// with no video is not tried again, and a site that refuses this client
// stops every fetch for an hour and records nothing.
func TestAFailedFetchBacksOffAndARefusalPausesEveryFetch(t *testing.T) {
	r := newRig(t,
		movie("a", "uid-a", true, map[string]string{"tmdb": "1"}),
		movie("b", "uid-b", true, map[string]string{"tmdb": "2"}),
	)
	r.videos = []trailers.Video{official}
	r.fetch = func(string) (ytdlp.Fetched, error) { return ytdlp.Fetched{}, errors.New("ytdlp: timed out") }

	r.pass()
	e, ok := r.entry(trailers.KindMovie, "uid-a")
	require.True(t, ok)
	assert.Equal(t, trailers.StateFailed, e.State)
	assert.Equal(t, 1, e.Attempts)
	assert.Contains(t, e.Error, "timed out")
	assert.Len(t, r.fetched, 2)

	r.pass()
	assert.Len(t, r.fetched, 2, "not before the backoff")
	r.clock.Advance(trailers.RetryAfter(1))
	r.fetch = func(string) (ytdlp.Fetched, error) { return ytdlp.Fetched{}, ytdlp.ErrUnavailable }
	r.pass()
	assert.Len(t, r.fetched, 4)
	e, _ = r.entry(trailers.KindMovie, "uid-a")
	assert.Equal(t, trailers.MaxAttempts, e.Attempts, "a page with no video is not tried again")
	r.clock.Advance(trailers.RetryAfter(trailers.MaxAttempts))
	r.pass()
	assert.Len(t, r.fetched, 4)

	// Two weeks on both are due again; the site refuses the first, and
	// the second is not asked for.
	r.clock.Advance(trailers.NoneRecheck)
	r.fetch = func(string) (ytdlp.Fetched, error) { return ytdlp.Fetched{}, ytdlp.ErrThrottled }
	r.pass()
	assert.Len(t, r.fetched, 5)
	e, _ = r.entry(trailers.KindMovie, "uid-a")
	assert.Equal(t, trailers.MaxAttempts, e.Attempts, "a refusal is not the video's failure")
	r.clock.Advance(throttlePause)
	r.pass()
	assert.Len(t, r.fetched, 6)
}

// A fetched file that is not a video with sound is a failed fetch, and
// nothing is placed.
func TestAFetchedFileThatIsNotAVideoWithSoundIsNotPlaced(t *testing.T) {
	r := newRig(t, movie("a", "uid-a", true, map[string]string{"tmdb": "1"}))
	r.videos = []trailers.Video{official}
	r.Probe = func(context.Context, string) (Facts, error) {
		return Facts{DurationMillis: 127000, VideoCodec: "h264"}, nil
	}
	r.pass()
	e, ok := r.entry(trailers.KindMovie, "uid-a")
	require.True(t, ok)
	assert.Equal(t, trailers.StateFailed, e.State)
	assert.NoFileExists(t, trailers.File(r.root, trailers.KindMovie, "uid-a"))
}

// A ready trailer whose file is gone is fetched again, and the record and
// file of an item that is gone are removed -- but never a file outside a
// root folder's trailers directory, and never anything when the catalog
// reads as empty.
func TestAGoneFileIsFetchedAgainAndAGoneItemsTrailerIsRemoved(t *testing.T) {
	a := movie("a", "uid-a", true, map[string]string{"tmdb": "1"})
	b := movie("b", "uid-b", true, map[string]string{"tmdb": "2"})
	r := newRig(t, a, b)
	r.videos = []trailers.Video{official}
	r.pass()
	fileA, fileB := trailers.File(r.root, trailers.KindMovie, "uid-a"), trailers.File(r.root, trailers.KindMovie, "uid-b")
	require.FileExists(t, fileA)
	require.FileExists(t, fileB)

	require.NoError(t, os.Remove(fileA))
	r.clock.Advance(verifyEvery)
	r.pass()
	require.FileExists(t, fileA)
	assert.Len(t, r.fetched, 3)

	// A record naming a file elsewhere, as a hand-edited one might.
	outside := filepath.Join(t.TempDir(), "keep.mp4")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o644))
	stray, err := trailers.Encode(trailers.Entry{State: trailers.StateReady, Path: outside})
	require.NoError(t, err)
	_, err = r.KV.Put(context.Background(), trailers.Key(trailers.KindMovie, "uid-stray"), stray)
	require.NoError(t, err)

	require.NoError(t, r.Reader.(client.Client).Delete(context.Background(), b))
	r.clock.Advance(forgetEvery)
	r.pass()
	assert.NoFileExists(t, fileB)
	_, ok := r.entry(trailers.KindMovie, "uid-b")
	assert.False(t, ok)
	_, ok = r.entry(trailers.KindMovie, "uid-stray")
	assert.False(t, ok)
	assert.FileExists(t, outside, "a file outside a trailers directory is never removed")
	require.FileExists(t, fileA)

	require.NoError(t, r.Reader.(client.Client).Delete(context.Background(), a))
	r.clock.Advance(forgetEvery)
	r.pass()
	assert.FileExists(t, fileA, "an empty catalog removes nothing")
}

// A trailer lies under the directory every library walk skips
// (fsops.ClustarrDir), so a root-folder scan never reports it as a file
// nobody owns.
func TestTrailersLieUnderTheDirectoryNoLibraryWalkEnters(t *testing.T) {
	assert.Equal(t, filepath.Join("/data/media/movies", fsops.ClustarrDir, "trailers"), trailers.Dir("/data/media/movies"))
}
