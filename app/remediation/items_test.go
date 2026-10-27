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

package remediation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	cataloghistory "github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func heatFile(gen int64, labels map[string]string, st catalogv1alpha1.MediaFileStatus) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat-1080p", Generation: gen, Labels: labels},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
			Path:     "/data/media/movies/Heat (1995)/Heat.mkv",
		},
		Status: st,
	}
}

func readyIs(s metav1.ConditionStatus) []metav1.Condition {
	return []metav1.Condition{{Type: catalogv1alpha1.MediaFileConditionReady, Status: s, Reason: "Probed"}}
}

// A status-only update wakes the file's items only when an input to their
// rollups moved (loop spec §3.3 S1', §2.14); a spec change always does.
func TestItemWakeKeys(t *testing.T) {
	heat := []Key{{Kind: KindMovie, Namespace: "media", Name: "heat"}}
	none := catalogv1alpha1.MediaFileStatus{}
	for _, tc := range []struct {
		name     string
		old, cur *catalogv1alpha1.MediaFile
		want     []Key
	}{
		{"a spec change", heatFile(1, nil, none), heatFile(2, nil, none), heat},
		{"the Transcoded verdict", heatFile(1, nil, none),
			heatFile(1, nil, catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{TranscodeProfile: "hevc@abc"}}), heat},
		{"the probed audio languages", heatFile(1, nil, catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
			Audio: []commonv1.AudioStream{{Language: "eng"}}}}),
			heatFile(1, nil, catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
				Audio: []commonv1.AudioStream{{Language: "eng"}, {Language: "jpn"}}}}), heat},
		{"probed at all", heatFile(1, nil, none), heatFile(1, nil, catalogv1alpha1.MediaFileStatus{ProbeHash: "p1"}), heat},
		{"a new probe hash alone", heatFile(1, nil, catalogv1alpha1.MediaFileStatus{ProbeHash: "p1"}),
			heatFile(1, nil, catalogv1alpha1.MediaFileStatus{ProbeHash: "p2"}), nil},
		{"Ready", heatFile(1, nil, catalogv1alpha1.MediaFileStatus{Conditions: readyIs(metav1.ConditionTrue)}),
			heatFile(1, nil, catalogv1alpha1.MediaFileStatus{Conditions: readyIs(metav1.ConditionFalse)}), heat},
		{"the transcode phase", heatFile(1, nil, catalogv1alpha1.MediaFileStatus{
			Transcode: &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseQueued}}),
			heatFile(1, nil, catalogv1alpha1.MediaFileStatus{
				Transcode: &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseRunning}}), heat},
		{"the graft phase", heatFile(1, nil, catalogv1alpha1.MediaFileStatus{
			Graft: &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting}}),
			heatFile(1, nil, catalogv1alpha1.MediaFileStatus{
				Graft: &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseRunning}}), heat},
		{"the graft block's tag", heatFile(1, nil, catalogv1alpha1.MediaFileStatus{
			Graft: &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseSucceeded}}),
			heatFile(1, nil, catalogv1alpha1.MediaFileStatus{
				Graft: &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseSucceeded, Tag: "graft@1"}}), heat},
		{"the flat graftTag", heatFile(1, nil, none), heatFile(1, nil, catalogv1alpha1.MediaFileStatus{GraftTag: "graft@1"}), heat},
		{"probe metadata outside the rollups", heatFile(1, nil, catalogv1alpha1.MediaFileStatus{ProbeVersion: 1}),
			heatFile(1, nil, catalogv1alpha1.MediaFileStatus{ProbeVersion: 2}), nil},
		{"the loop's own sequence", heatFile(1, nil, catalogv1alpha1.MediaFileStatus{LastSeq: 1}),
			heatFile(1, nil, catalogv1alpha1.MediaFileStatus{LastSeq: 2}), nil},
		{"naming", heatFile(1, nil, none), heatFile(1, nil, catalogv1alpha1.MediaFileStatus{
			Naming: &catalogv1alpha1.NamingStatus{ExpectedPath: "/x.mkv"}}), nil},
		{"a label", heatFile(1, nil, none), heatFile(1, map[string]string{"tier": "hd"}, none), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, itemWakeKeys(tc.old, tc.cur))
		})
	}
	assert.Equal(t, heat, itemWakeKeys(nil, heatFile(1, nil, none)), "a missing old object reads as a create")
}

// Marking a transcoded file upgradeable moves its Transcoded verdict, so it
// wakes the file's items, which then read it below the cutoff; an
// annotation otherwise wakes none.
func TestMarkingATranscodeUpgradeableWakesItsItems(t *testing.T) {
	final := heatFile(1, nil, catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{TranscodeProfile: "hevc@abc"}})
	marked := final.DeepCopy()
	marked.Annotations = map[string]string{catalogv1alpha1.AnnotationUpgradeable: "true"}
	assert.Equal(t, []Key{{Kind: KindMovie, Namespace: "media", Name: "heat"}}, itemWakeKeys(final, marked))
}

// TestFileSignatureMovesWithTheGraftBlock (loop spec §3.3 S1'): the item
// key wakes when its file's graft phase or tag changes, which is how
// status.audio.graft follows the graft; a graft message alone does not.
func TestFileSignatureMovesWithTheGraftBlock(t *testing.T) {
	mf := &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{ProbeHash: "h1"}}
	base := SignatureOf(mf)
	waiting := mf.DeepCopy()
	waiting.Status.Graft = &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting}
	assert.NotEqual(t, base, SignatureOf(waiting))
	failed := waiting.DeepCopy()
	failed.Status.Graft.Phase = catalogv1alpha1.GraftPhaseFailed
	assert.NotEqual(t, SignatureOf(waiting), SignatureOf(failed))
	tagged := mf.DeepCopy()
	tagged.Status.GraftTag = "tag-1"
	assert.NotEqual(t, base, SignatureOf(tagged))
	blockTag := waiting.DeepCopy()
	blockTag.Status.Graft.Tag = "tag-1"
	assert.NotEqual(t, SignatureOf(waiting), SignatureOf(blockTag))
	message := waiting.DeepCopy()
	message.Status.Graft.Message = "waiting for a graft slot"
	assert.Equal(t, SignatureOf(waiting), SignatureOf(message), "a graft message alone wakes no item")
}

type recordingItem struct {
	res  reconcile.Result
	seen []types.NamespacedName
}

func (r *recordingItem) ReconcileItem(_ context.Context, nn types.NamespacedName) (reconcile.Result, error) {
	r.seen = append(r.seen, nn)
	return r.res, nil
}

func (r *recordingItem) Watches() []rollup.Watch { return nil }

// An item key reaches its kind's path, and the timer it asks for is a Due
// requeue (loop spec §3.11); a file key never reaches the item path.
func TestItemReconcilerDispatchesByKind(t *testing.T) {
	movies := &recordingItem{res: reconcile.Result{RequeueAfter: time.Hour}}
	ir := &ItemReconciler{Items: map[KeyKind]rollup.Item{KindMovie: movies}}
	res, err := ir.Reconcile(context.Background(), Key{Kind: KindMovie, Namespace: "media", Name: "heat"})
	require.NoError(t, err)
	assert.Equal(t, []types.NamespacedName{{Namespace: "media", Name: "heat"}}, movies.seen)
	assert.Equal(t, time.Hour, res.RequeueAfter)
	require.NotNil(t, res.Priority)
	assert.Equal(t, PriorityTimed, *res.Priority)

	movies.res = reconcile.Result{}
	res, err = ir.Reconcile(context.Background(), Key{Kind: KindMovie, Namespace: "media", Name: "heat"})
	require.NoError(t, err)
	assert.Nil(t, res.Priority, "no timer, no priority")

	_, err = ir.Reconcile(context.Background(), Key{Kind: KindAlbum, Namespace: "media", Name: "x"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, reconcile.TerminalError(nil)), "a kind with no path is not retried")

	r := &Reconciler{Items: *ir}
	_, err = r.Reconcile(context.Background(), Key{Kind: KindMovie, Namespace: "media", Name: "ridley"})
	require.NoError(t, err)
	assert.Equal(t, types.NamespacedName{Namespace: "media", Name: "ridley"}, movies.seen[len(movies.seen)-1], "the loop routes an item key to Items")
}

// TestSeriesAndComicAreItemKeys (ADR-0019 A3.2): the owners of their
// episodes' and issues' grabs are loop keys like the six F4 kinds: a
// MediaRef kind, a cached object, an ItemRef and a history target of
// either kind all map to a key.
func TestSeriesAndComicAreItemKeys(t *testing.T) {
	for mk, want := range map[commonv1.MediaKind]KeyKind{commonv1.MediaKindSeries: KindSeries, commonv1.MediaKindComic: KindComic} {
		got, ok := ItemKind(mk)
		require.True(t, ok, mk)
		assert.Equal(t, want, got)
		obj, ok := newItemObject(got)
		require.True(t, ok)
		switch want {
		case KindSeries:
			assert.IsType(t, &catalogv1alpha1.Series{}, obj)
		case KindComic:
			assert.IsType(t, &catalogv1alpha1.Comic{}, obj)
		}
		k, ok := itemRefKey(schema.ItemRef{Kind: string(want), Namespace: "media", Name: "x"})
		require.True(t, ok)
		assert.Equal(t, Key{Kind: want, Namespace: "media", Name: "x"}, k)
		k, ok = targetKey(cataloghistory.Target{Kind: string(want), Namespace: "media", Name: "x"})
		require.True(t, ok)
		assert.Equal(t, Key{Kind: want, Namespace: "media", Name: "x"}, k)
	}
	assert.Equal(t, KeyKind("Series"), KindSeries)
	assert.Equal(t, KeyKind("Comic"), KindComic)
	for _, mk := range []commonv1.MediaKind{commonv1.MediaKindArtist, commonv1.MediaKindAuthor} {
		_, ok := ItemKind(mk)
		assert.False(t, ok, "%s holds no grabs of its own", mk)
	}
	_, ok := itemRefKey(schema.ItemRef{Kind: "MediaFile", Namespace: "media", Name: "x"})
	assert.False(t, ok, "a file is not an item key")
	_, ok = itemRefKey(schema.ItemRef{Kind: string(KindSeries), Namespace: "media"})
	assert.False(t, ok, "a ref with no name names no item")
}

// update is an update event from o to n with distinct resourceVersions:
// TypedFuncs reads an update whose resourceVersion did not move as a resync
// and enqueues it at handler.LowPriority.
func update(o, n client.Object) event.TypedUpdateEvent[client.Object] {
	o, n = o.DeepCopyObject().(client.Object), n.DeepCopyObject().(client.Object)
	o.SetResourceVersion("1")
	n.SetResourceVersion("2")
	return event.TypedUpdateEvent[client.Object]{ObjectOld: o, ObjectNew: n}
}

// recordingQueue records what a handler enqueues and at which priority.
type recordingQueue struct {
	priorityqueue.PriorityQueue[Key]
	added map[Key]int
}

func newRecordingQueue() *recordingQueue { return &recordingQueue{added: map[Key]int{}} }

func (q *recordingQueue) Add(k Key) { q.added[k] = 0 }

func (q *recordingQueue) AddWithOpts(o priorityqueue.AddOpts, ks ...Key) {
	for _, k := range ks {
		p := 0
		if o.Priority != nil {
			p = *o.Priority
		}
		q.added[k] = p
	}
}

// indexBuilder hands each index RegisterIndexes-style functions register to
// a fake client builder: the extractors are the real ones.
type indexBuilder struct{ b *fake.ClientBuilder }

func (i indexBuilder) IndexField(_ context.Context, obj client.Object, field string, fn client.IndexerFunc) error {
	i.b.WithIndex(obj, field, fn)
	return nil
}

func episodeOf(name, series string, season, number int32) *catalogv1alpha1.Episode {
	return &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series, SeasonNumber: season, EpisodeNumber: number},
	}
}

// TestS24WakesOnlyTheCoveredEpisodes (ADR-0019 §6.11): a Series whose
// entries changed wakes the Episodes its old and new entries cover, through
// .spec.seriesRef -- never its other episodes, never another series' --
// and a Series status write that moves no entry wakes nothing. Comic does
// the same for its Issues.
func TestS24WakesOnlyTheCoveredEpisodes(t *testing.T) {
	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme())
	require.NoError(t, series.RegisterIndexes(context.Background(), indexBuilder{b}))
	require.NoError(t, comic.RegisterIndexes(context.Background(), indexBuilder{b}))
	issue := func(name, comicRef, number string) *catalogv1alpha1.Issue {
		return &catalogv1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name},
			Spec: catalogv1alpha1.IssueSpec{ComicRef: comicRef, Number: number}}
	}
	c := b.WithObjects(
		episodeOf("show-s01e01", "show", 1, 1), episodeOf("show-s01e02", "show", 1, 2), episodeOf("show-s01e03", "show", 1, 3),
		episodeOf("show-s02e01", "show", 2, 1), episodeOf("other-s01e01", "other", 1, 1),
		issue("saga-1", "saga", "1"), issue("saga-2", "saga", "2"), issue("x-men-1", "x-men", "1"),
	).Build()
	ep := func(s, n int32) catalogv1alpha1.EpisodeNumber { return catalogv1alpha1.EpisodeNumber{Season: s, Number: n} }
	show := func(entries ...catalogv1alpha1.DownloadEntry) *catalogv1alpha1.Series {
		s := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "show"}}
		s.Status.Downloads = entries
		return s
	}
	key := func(kind KeyKind, name string) Key { return Key{Kind: kind, Namespace: "media", Name: name} }

	h := containerWakes(c, KindSeries)
	q := newRecordingQueue()
	old := show(catalogv1alpha1.DownloadEntry{ID: "a", Phase: commonv1.DownloadPhaseDownloading, Episodes: []catalogv1alpha1.EpisodeNumber{ep(1, 1)}})
	cur := show(catalogv1alpha1.DownloadEntry{ID: "a", Phase: commonv1.DownloadPhaseImported, Episodes: []catalogv1alpha1.EpisodeNumber{ep(1, 1)}},
		catalogv1alpha1.DownloadEntry{ID: "b", Phase: commonv1.DownloadPhaseQueued, Episodes: []catalogv1alpha1.EpisodeNumber{ep(1, 2), ep(2, 1)}})
	h.Update(context.Background(), update(old, cur), q)
	assert.Equal(t, map[Key]int{key(KindEpisode, "show-s01e01"): 0, key(KindEpisode, "show-s01e02"): 0, key(KindEpisode, "show-s02e01"): 0}, q.added)

	q = newRecordingQueue()
	gone := show()
	h.Update(context.Background(), update(cur, gone), q)
	assert.Equal(t, map[Key]int{key(KindEpisode, "show-s01e01"): 0, key(KindEpisode, "show-s01e02"): 0, key(KindEpisode, "show-s02e01"): 0}, q.added,
		"entries that went wake the episodes they covered")

	q = newRecordingQueue()
	same := cur.DeepCopy()
	same.Status.EpisodeCount = 9
	h.Update(context.Background(), update(cur, same), q)
	assert.Empty(t, q.added, "a write that moves no entry wakes nothing")

	hc := containerWakes(c, KindComic)
	q = newRecordingQueue()
	oc := &catalogv1alpha1.Comic{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "saga"}}
	nc := oc.DeepCopy()
	nc.Status.Downloads = []catalogv1alpha1.DownloadEntry{{ID: "c", Phase: commonv1.DownloadPhaseQueued, Issues: []string{"2"}}}
	hc.Update(context.Background(), update(oc, nc), q)
	assert.Equal(t, map[Key]int{key(KindIssue, "saga-2"): 0}, q.added)
}
