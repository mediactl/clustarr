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

package nfo_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	nfoworker "github.com/mediactl/clustarr/app/import/worker/nfo"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/nfo"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgnfo "github.com/mediactl/clustarr/pkg/nfo"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// The planner-to-worker loop on the bus (2026-11-10 design §8.2, D13): the
// stage renders an Album's album.nfo, writes the clustarr-nfo request and
// publishes the NFOWriteTask on import-nfo; the import agent's worker, bound
// to that durable as the import domain binds it, writes the bytes into the
// album's folder and answers the record; the next pass incorporates the
// answer into status.nfo under catalog-nfo and releases the dispatch; and
// once status.nfo records the hash, a pass asks for nothing more.
func TestThePlannerToWorkerLoopOnTheBus(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	musicRoot := filepath.Join(dataDir, "music")
	folder := filepath.Join(musicRoot, "Muse", "Absolution (2003)")
	released := metav1.NewTime(time.Date(2003, 9, 15, 0, 0, 0, 0, time.UTC))

	root := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "music", Namespace: "media"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: musicRoot, Kind: catalogv1alpha1.RootFolderKindMusic},
	}
	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "muse", Namespace: "media", UID: "a1"},
		Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "9c9f1980-dc94-4cd2-a64b-0c5a0a00f8e4", QualityProfileRef: "music-lossless", RootFolderRef: root.Name},
		Status:     catalogv1alpha1.ArtistStatus{Metadata: &catalogv1alpha1.ArtistMetadata{Name: "Muse"}, Path: filepath.Join(musicRoot, "Muse")},
	}
	album := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "muse-absolution", Namespace: "media", UID: "u1"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: artist.Name, ReleaseGroupID: "rg-absolution"},
		Status: catalogv1alpha1.AlbumStatus{
			Metadata: &catalogv1alpha1.AlbumMetadata{Title: "Absolution", ReleaseDate: &released},
			Tracks:   []catalogv1alpha1.Track{{RecordingID: "r1", Medium: 1, Number: 1, Title: "Intro"}, {RecordingID: "r2", Medium: 1, Number: 2, Title: "Apocalypse Please"}},
			Path:     folder,
		},
	}
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(root, artist, album).Build()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	st := nfo.New(nfo.Options{Reader: c, Bus: bus})
	view := func() *remediation.ItemView {
		return &remediation.ItemView{
			Key:  remediation.Key{Kind: remediation.KindAlbum, Namespace: album.Namespace, Name: album.Name},
			Item: album.DeepCopy(), Now: metav1.NewTime(now),
		}
	}

	// The import agent's worker, bound as app/import/agent binds it.
	spec, ok := events.Default().Consumer(events.ConsumerImportNFO)
	require.True(t, ok)
	stop, err := bus.Subscribe(ctx, spec.Subscription(), nfoworker.New(bus, dataDir, "import-agent-0").Handle)
	require.NoError(t, err)
	t.Cleanup(stop)

	// Pass 1: the request and the task.
	res, err := st.Plan(ctx, &remediation.Env{}, view())
	require.NoError(t, err)
	require.Len(t, res.Effects, 2, "the request record, then the task: %+v", res.Effects)
	assert.Empty(t, res.Sets, "nothing to write until an answer")
	rw, ok := res.Effects[0].(remediation.RecordWrite)
	require.True(t, ok)
	assert.Equal(t, events.BucketNFO, rw.Bucket)
	assert.Equal(t, agentrecords.NFOKey("u1"), rw.Key)
	assert.Zero(t, rw.Revision, "a first request creates the record")
	_, err = bus.KV(rw.Bucket).Put(ctx, rw.Key, rw.Value)
	require.NoError(t, err)
	pub, ok := res.Effects[1].(remediation.DispatchPublish)
	require.True(t, ok)
	assert.Equal(t, nfo.Durable, pub.Durable)
	assert.Equal(t, events.WorkNFOSubject("Album", "u1"), pub.Publish.Subject)
	assert.Equal(t, events.StreamWorkImport, pub.Publish.ExpectStream)
	var task schema.NFOWriteTask
	require.NoError(t, schema.Decode(pub.Publish.Envelope.Schema, pub.Publish.Envelope.Data, &task))
	assert.Equal(t, pub.Seq, task.Seq)
	assert.Equal(t, folder, task.Folder)
	require.Len(t, task.Files, 1)
	assert.Equal(t, pkgnfo.FileAlbum, task.Files[0].Name)
	assert.Contains(t, string(task.Files[0].Content), "<title>Absolution</title>")
	assert.Contains(t, string(task.Files[0].Content), "Muse")
	assert.Empty(t, task.Images, "no poster in the artwork store: no folder.jpg")
	assert.Empty(t, task.Owned, "a first write owns nothing yet")

	// The task on the bus: the worker writes the folder and answers.
	_, err = bus.Publish(ctx, pub.Publish.Subject, pub.Publish.Envelope,
		events.WithMsgID(pub.Publish.MsgID), events.WithExpectStream(pub.Publish.ExpectStream))
	require.NoError(t, err)
	rd := records.NewRequester(bus.KV(events.BucketNFO), agentrecords.NFO())
	var rec *schema.NFORecord
	require.Eventually(t, func() bool {
		var found bool
		rec, _, found, err = rd.Get(ctx, agentrecords.NFOKey("u1"))
		return err == nil && found && rec.State == records.StateAnswered
	}, 10*time.Second, 20*time.Millisecond, "the worker never answered the record")
	assert.Equal(t, task.Seq, rec.Seq)
	require.NotNil(t, rec.Answer)
	assert.Equal(t, []string{pkgnfo.FileAlbum}, rec.Answer.Files)
	written, err := os.ReadFile(filepath.Join(folder, pkgnfo.FileAlbum))
	require.NoError(t, err)
	assert.Equal(t, task.Files[0].Content, written, "the manager's bytes, as rendered")

	// Pass 2: the answer is incorporated under the stage's own manager and
	// the dispatch released; nothing is asked again.
	res, err = st.Plan(ctx, &remediation.Env{}, view())
	require.NoError(t, err)
	require.Len(t, res.Sets, 1)
	assert.Equal(t, k8s.ManagerCatalogNFO, res.Sets[0].Manager)
	status, ok := res.Sets[0].Fields["nfo"].(*catalogv1alpha1.NFOStatus)
	require.True(t, ok, "%T", res.Sets[0].Fields["nfo"])
	assert.Equal(t, task.Hash, status.Hash)
	assert.Equal(t, folder, status.Path)
	assert.Equal(t, []string{pkgnfo.FileAlbum}, status.Files)
	require.NotNil(t, status.WrittenAt)
	require.Len(t, res.Effects, 1, "%+v", res.Effects)
	ans, ok := res.Effects[0].(remediation.DispatchAnswered)
	require.True(t, ok, "%T", res.Effects[0])
	assert.Equal(t, nfo.Durable, ans.Durable)
	assert.Equal(t, task.Seq, ans.Seq)

	// Pass 3, status.nfo applied: the hash is recorded, so no new request.
	album.Status.NFO = status
	res, err = st.Plan(ctx, &remediation.Env{}, view())
	require.NoError(t, err)
	assert.Empty(t, res.Sets)
	for _, e := range res.Effects {
		switch e.(type) {
		case remediation.RecordWrite, remediation.DispatchPublish:
			t.Errorf("a recorded document was asked for again: %+v", e)
		}
	}
}
