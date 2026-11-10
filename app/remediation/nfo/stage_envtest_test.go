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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	nfoworker "github.com/mediactl/clustarr/app/import/worker/nfo"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/itemtest/looptest"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgnfo "github.com/mediactl/clustarr/pkg/nfo"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// testCfg is the package's envtest control plane; nil when
// KUBEBUILDER_ASSETS is unset, and the envtest then skips (its siblings'
// rule: app/remediation/search's season_envtest_test.go).
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest: " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		panic("stop envtest: " + err.Error())
	}
	os.Exit(code)
}

// mediaTempDir is a fresh directory under /data/media, which
// RootFolder.spec.path's CEL rule requires (app/import/worker/rescan's
// helper of the same name): a missing or read-only /data/media is a named
// skip, not a failure, like the apiserver assets.
func mediaTempDir(t *testing.T) string {
	t.Helper()
	prefix := os.Getenv("CLUSTARR_TEST_MEDIA_ROOT")
	if prefix == "" {
		prefix = "/data/media"
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Skipf("%s is not creatable (%v); RootFolder.spec.path must start with /data/media/", prefix, err)
	}
	dir, err := os.MkdirTemp(prefix, "clustarr-nfo-")
	if err != nil {
		t.Skipf("%s is not writable (%v); RootFolder.spec.path must start with /data/media/", prefix, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// seedStatus applies status under manager, standing in for the writer the
// test does not run.
func seedStatus(t *testing.T, c client.Client, obj client.Object, manager k8s.FieldManager, status map[string]any) {
	t.Helper()
	gvk, err := apiutil.GVKForObject(obj, c.Scheme())
	require.NoError(t, err)
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(), "kind": gvk.Kind,
		"metadata": map[string]any{"name": obj.GetName(), "namespace": obj.GetNamespace()},
		"status":   status,
	}}
	_, err = k8s.PatchStatusUnstructured(context.Background(), c, manager, u)
	require.NoError(t, err)
}

func ensureMusicProfile(t *testing.T, c client.Client) {
	t.Helper()
	doc, err := catalogue.ProfileFS().ReadFile("data/profiles/music-lossless.json")
	require.NoError(t, err)
	seeds, err := quality.DecodeProfileSeeds(doc)
	require.NoError(t, err)
	err = c.Create(context.Background(), &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "music-lossless"}, Spec: seeds[0].Spec})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
}

// statusOwnerOf is the field manager owning status.<field> on obj, "" when
// nobody does: the one place the apiserver records which writer a status
// leaf belongs to.
func statusOwnerOf(t *testing.T, entries []metav1.ManagedFieldsEntry, field string) string {
	t.Helper()
	for _, e := range entries {
		if e.Subresource != "status" || e.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		status, ok := fields["f:status"].(map[string]any)
		if !ok {
			continue
		}
		if _, owns := status["f:"+field]; owns {
			return e.Manager
		}
	}
	return ""
}

// message is one delivery of a published envelope to the import agent's
// worker, as the import-nfo durable would hand it over.
type message struct{ env *events.Envelope }

func (m *message) Envelope() *events.Envelope               { return m.env }
func (m *message) Subject() string                          { return "" }
func (m *message) Attempt() uint64                          { return 1 }
func (m *message) Ack(context.Context) error                { return nil }
func (m *message) Nak(context.Context, time.Duration) error { return nil }
func (m *message) Term(context.Context, string) error       { return nil }
func (m *message) InProgress(context.Context) error         { return nil }

// The nfo stage in the loop (2026-11-10 design §8.2, D13): an Album whose
// folder the Album pass resolved under a music root gets one NFOWriteTask on
// import-nfo with its album.nfo rendered from the catalog; the import
// agent's worker writes it into the folder and answers in clustarr-nfo,
// which wakes the Album; the next pass records the hash, path and files in
// status.nfo under catalog-nfo, the stage's own manager; and with the hash
// recorded the Album is asked for nothing more.
func TestTheNFOStageWritesAnAlbumsNFOThroughTheImportAgent(t *testing.T) {
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	root := mediaTempDir(t)
	l := looptest.Start(t, testCfg, looptest.Options{})
	ctx := l.Ctx
	const ns = "nfo-album"
	require.NoError(t, l.API.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	ensureMusicProfile(t, l.API)

	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "music", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: root, Kind: catalogv1alpha1.RootFolderKindMusic},
	}
	require.NoError(t, l.API.Create(ctx, rf))
	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "muse", Namespace: ns},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "9c9f1980-dc94-4cd2-a64b-0c5a0a00f8e4", QualityProfileRef: "music-lossless", RootFolderRef: rf.Name,
		},
	}
	require.NoError(t, l.API.Create(ctx, artist))
	seedStatus(t, l.API, artist, k8s.ManagerCatalogMetadata, map[string]any{"metadata": map[string]any{"name": "Muse"}})
	seedStatus(t, l.API, artist, k8s.ManagerCatalog, map[string]any{"path": filepath.Join(root, "Muse")})
	album := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "muse-absolution", Namespace: ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: artist.Name, ReleaseGroupID: "b1392450-e666-3926-a536-22c65f834433"},
	}
	require.NoError(t, l.API.Create(ctx, album))
	seedStatus(t, l.API, album, k8s.ManagerCatalogMetadata, map[string]any{"metadata": map[string]any{
		"title": "Absolution", "releaseDate": "2003-09-15T00:00:00Z",
		"refreshedAt": l.Clock.Now().UTC().Format(time.RFC3339),
	}})
	for _, o := range []client.Object{rf, artist, album} {
		l.Synced(t, o)
	}
	key := remediation.Key{Kind: remediation.KindAlbum, Namespace: ns, Name: album.Name}
	get := func() {
		t.Helper()
		require.NoError(t, l.API.Get(ctx, client.ObjectKeyFromObject(album), album))
	}
	nfoTasks := func() []looptest.Published { return l.Bus.Published("clustarr.work.import.nfo.") }

	// Pass 1: the Album's rollup resolves its folder from its Artist's;
	// the stage plans from the cached item, so its turn is the next pass.
	l.Pass(t, remediation.KindAlbum, ns, album.Name)
	get()
	require.NotEmpty(t, album.Status.Path, "the Album pass resolved no folder")
	assert.True(t, strings.HasPrefix(album.Status.Path, filepath.Join(root, "Muse")+string(filepath.Separator)), album.Status.Path)
	l.Synced(t, album)

	// Pass 2: the request and the task.
	l.Pass(t, remediation.KindAlbum, ns, album.Name)
	pubs := nfoTasks()
	require.Len(t, pubs, 1, "one NFOWriteTask")
	assert.Equal(t, events.WorkNFOSubject("Album", string(album.UID)), pubs[0].Subject)
	var task schema.NFOWriteTask
	require.NoError(t, schema.Decode(pubs[0].Env.Schema, pubs[0].Env.Data, &task))
	assert.Equal(t, album.Status.Path, task.Folder)
	assert.Equal(t, string(album.UID), task.Item.UID)
	require.Len(t, task.Files, 1)
	assert.Equal(t, pkgnfo.FileAlbum, task.Files[0].Name)
	assert.Contains(t, string(task.Files[0].Content), "<title>Absolution</title>")
	assert.Contains(t, string(task.Files[0].Content), "Muse")
	rd := records.NewRequester(l.Bus.KV(events.BucketNFO), agentrecords.NFO())
	rec, _, ok, err := rd.Get(ctx, agentrecords.NFOKey(string(album.UID)))
	require.NoError(t, err)
	require.True(t, ok, "the request record")
	assert.Equal(t, records.StateRequested, rec.State)
	assert.Equal(t, task.Seq, rec.Seq)
	assert.Equal(t, task.Hash, rec.Request.Hash)
	get()
	assert.Nil(t, album.Status.NFO, "nothing recorded until the agent answers")

	// The import agent writes the folder and answers; the answer wakes the
	// Album through the stage's record source.
	woken := l.Woken.Count(key)
	w := nfoworker.New(l.Bus, root, "import-agent-0")
	require.NoError(t, w.Handle(ctx, &message{env: pubs[0].Env}))
	written, err := os.ReadFile(filepath.Join(album.Status.Path, pkgnfo.FileAlbum))
	require.NoError(t, err)
	assert.Equal(t, task.Files[0].Content, written, "the manager's bytes, as rendered")
	require.Eventually(t, func() bool { return l.Woken.Count(key) > woken }, 5*time.Second, 20*time.Millisecond,
		"the answer in clustarr-nfo never woke the Album")

	// Pass 3: status.nfo under catalog-nfo.
	l.Pass(t, remediation.KindAlbum, ns, album.Name)
	get()
	require.NotNil(t, album.Status.NFO, "status.nfo")
	assert.Equal(t, task.Hash, album.Status.NFO.Hash)
	assert.Equal(t, album.Status.Path, album.Status.NFO.Path)
	assert.Equal(t, []string{pkgnfo.FileAlbum}, album.Status.NFO.Files)
	assert.NotNil(t, album.Status.NFO.WrittenAt)
	assert.Equal(t, string(k8s.ManagerCatalogNFO), statusOwnerOf(t, album.ManagedFields, "nfo"), "status.nfo is the stage's own field")
	assert.NotEqual(t, string(k8s.ManagerCatalogNFO), statusOwnerOf(t, album.ManagedFields, "path"), "the stage owns nothing else")
	l.Synced(t, album)

	// Pass 4: the hash is recorded, so nothing is asked again.
	l.Pass(t, remediation.KindAlbum, ns, album.Name)
	assert.Len(t, nfoTasks(), 1, "a recorded document is written once")
}
