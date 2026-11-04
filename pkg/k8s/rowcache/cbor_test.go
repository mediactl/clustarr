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

package rowcache

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/cbor"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/runtime/serializer/streaming"
	"k8s.io/apimachinery/pkg/watch"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// episodeKind resolves the Episode kind as New does, without a mapper.
func episodeKind(t *testing.T, scheme *runtime.Scheme) *kind {
	t.Helper()
	gvk := catalogv1alpha1.GroupVersion.WithKind("Episode")
	typ := reflect.TypeFor[catalogv1alpha1.Episode]()
	c, err := newCodec(gvk.Kind, gvk, typ)
	require.NoError(t, err)
	return &kind{gvk: gvk, resource: "episodes", typ: typ, obj: &catalogv1alpha1.Episode{}, codec: c}
}

// The row cache's own lister-watcher asks the apiserver for CBOR and decodes
// what it answers; with ClientsAllowCBOR off, client-go sends the same
// request as JSON.
func TestTheKindListsOverCBOR(t *testing.T) {
	scheme := k8s.MustNewScheme()
	list := &catalogv1alpha1.EpisodeList{
		TypeMeta: metav1.TypeMeta{APIVersion: "catalog.clustarr.io/v1alpha1", Kind: "EpisodeList"},
		ListMeta: metav1.ListMeta{ResourceVersion: "7"},
		Items:    []catalogv1alpha1.Episode{*episode("media", "a", 1), *episode("media", "a", 2)},
	}
	body, err := runtime.Encode(cbor.NewSerializer(scheme, scheme), list)
	require.NoError(t, err)
	jsonBody, err := runtime.Encode(serializerJSON(scheme), list)
	require.NoError(t, err)

	var accepts []string
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepts = append(accepts, r.Header.Get("Accept"))
		paths = append(paths, r.URL.Path)
		if strings.HasPrefix(r.Header.Get("Accept"), runtime.ContentTypeCBOR) {
			w.Header().Set("Content-Type", runtime.ContentTypeCBOR)
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", runtime.ContentTypeJSON)
		_, _ = w.Write(jsonBody)
	}))
	defer srv.Close()

	k := episodeKind(t, scheme)
	lw, err := cborListerWatcher(&rest.Config{Host: srv.URL}, nil, scheme, k, "media")
	require.NoError(t, err)
	got, err := listWith(t, lw, metav1.ListOptions{})
	require.NoError(t, err)
	eps, ok := got.(*catalogv1alpha1.EpisodeList)
	require.True(t, ok, "got %T", got)
	require.Len(t, eps.Items, 2)
	assert.Equal(t, "a-s01e002", eps.Items[1].Name)
	assert.Equal(t, "7", eps.ResourceVersion)
	require.Len(t, accepts, 1)
	assert.Equal(t, runtime.ContentTypeCBOR+","+runtime.ContentTypeJSON, accepts[0], "CBOR first, JSON as the fallback")
	assert.Equal(t, "/apis/catalog.clustarr.io/v1alpha1/namespaces/media/episodes", paths[0])

	// With the gate off, client-go rewrites the content types to JSON.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.ClientsAllowCBOR, false)
	assert.False(t, cborAllowed())
	lw, err = cborListerWatcher(&rest.Config{Host: srv.URL}, nil, scheme, k, "")
	require.NoError(t, err)
	got, err = listWith(t, lw, metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, got.(*catalogv1alpha1.EpisodeList).Items, 2)
	require.Len(t, accepts, 2)
	assert.NotContains(t, accepts[1], "cbor")
	assert.Equal(t, "/apis/catalog.clustarr.io/v1alpha1/episodes", paths[1], "every namespace")
}

// The gate is on by default in this process (init; the environment's say at
// the process's start is read by client-go before it, and cannot be tested
// from inside the process), and PreferCBOR stays off.
func TestClientsAllowCBORIsOnByDefault(t *testing.T) {
	assert.True(t, cborAllowed())
	assert.False(t, clientfeatures.FeatureGates().Enabled(clientfeatures.ClientsPreferCBOR), "PreferCBOR stays off: controller-runtime's clients carry no CBOR encoder")
}

// The watch streams as a CBOR sequence, which client-go decodes behind the
// gate into the kind's objects: the path a watch-list's initial events take.
func TestTheKindWatchesOverACBORSequence(t *testing.T) {
	scheme := k8s.MustNewScheme()
	eps := []*catalogv1alpha1.Episode{episode("media", "a", 1), episode("media", "a", 2)}
	for _, ep := range eps {
		ep.APIVersion, ep.Kind = "catalog.clustarr.io/v1alpha1", "Episode"
	}
	var accept, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept, path = r.Header.Get("Accept"), r.URL.RawQuery
		w.Header().Set("Content-Type", runtime.ContentTypeCBORSequence)
		w.WriteHeader(http.StatusOK)
		info := cbor.NewSerializerInfo(scheme, scheme)
		enc := streaming.NewEncoder(info.StreamSerializer.Framer.NewFrameWriter(w), info.StreamSerializer.Serializer)
		for _, ep := range eps {
			require.NoError(t, enc.Encode(&metav1.WatchEvent{Type: string(watch.Added), Object: runtime.RawExtension{Object: ep}}))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	k := episodeKind(t, scheme)
	lw, err := cborListerWatcher(&rest.Config{Host: srv.URL}, nil, scheme, k, "media")
	require.NoError(t, err)
	w, err := toolscache.ToListerWatcherWithContext(lw).WatchWithContext(t.Context(), metav1.ListOptions{ResourceVersion: "7"})
	require.NoError(t, err)
	defer w.Stop()
	for i := range eps {
		select {
		case e := <-w.ResultChan():
			assert.Equal(t, watch.Added, e.Type)
			got, ok := e.Object.(*catalogv1alpha1.Episode)
			require.True(t, ok, "event %d carries a %T", i, e.Object)
			assert.Equal(t, eps[i].Name, got.Name)
			assert.Equal(t, eps[i].Spec, got.Spec)
		case <-time.After(5 * time.Second):
			t.Fatalf("no event %d", i)
		}
	}
	assert.Equal(t, runtime.ContentTypeCBOR+","+runtime.ContentTypeJSON, accept)
	assert.Contains(t, path, "watch=true")
	assert.Contains(t, path, "resourceVersion=7")
}

// A kind keeps controller-runtime's lister-watcher when the cache's options
// give it a shape the row cache's own does not reproduce.
func TestOnlyAPlainCacheShapeStreamsOverCBOR(t *testing.T) {
	k := episodeKind(t, k8s.MustNewScheme())
	ns, ok := cborNamespace(cache.Options{}, k)
	assert.True(t, ok)
	assert.Equal(t, "", ns)
	ns, ok = cborNamespace(cache.Options{DefaultNamespaces: map[string]cache.Config{"media": {}}}, k)
	assert.True(t, ok)
	assert.Equal(t, "media", ns)
	_, ok = cborNamespace(cache.Options{DefaultNamespaces: map[string]cache.Config{cache.AllNamespaces: {}}}, k)
	assert.True(t, ok)
	_, ok = cborNamespace(cache.Options{DefaultNamespaces: map[string]cache.Config{"a": {}, "b": {}}}, k)
	assert.False(t, ok, "two namespaces are two informers the hook cannot tell apart")
	_, ok = cborNamespace(cache.Options{DefaultFieldSelector: fields.OneTermEqualSelector("metadata.name", "x")}, k)
	assert.False(t, ok)
	_, ok = cborNamespace(cache.Options{ByObject: map[client.Object]cache.ByObject{&catalogv1alpha1.Episode{}: {}}}, k)
	assert.False(t, ok)
	_, ok = cborNamespace(cache.Options{ByObject: map[client.Object]cache.ByObject{&catalogv1alpha1.Movie{}: {}}}, k)
	assert.True(t, ok, "another kind's entry is not this kind's")
}

// serializerJSON is a plain JSON encoder for scheme, the apiserver's other
// answer.
func serializerJSON(scheme *runtime.Scheme) runtime.Encoder {
	return json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme, scheme, json.SerializerOptions{})
}

// listWith lists through lw with a context.
func listWith(t *testing.T, lw toolscache.ListerWatcher, opts metav1.ListOptions) (runtime.Object, error) {
	t.Helper()
	return toolscache.ToListerWatcherWithContext(lw).ListWithContext(t.Context(), opts)
}
