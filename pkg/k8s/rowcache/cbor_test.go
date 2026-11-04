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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/runtime/serializer/cbor"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/runtime/serializer/streaming"
	"k8s.io/apimachinery/pkg/watch"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Both gates are on by default in this process (init; the environment's
// say at the process's start is read by client-go before it, and cannot be
// tested from inside the process).
func TestTheCBORGatesAreOnByDefault(t *testing.T) {
	assert.True(t, clientfeatures.FeatureGates().Enabled(clientfeatures.ClientsAllowCBOR))
	assert.True(t, clientfeatures.FeatureGates().Enabled(clientfeatures.ClientsPreferCBOR))
}

// cborAPIServer answers a list of two episodes in whatever the client asked
// for first, CBOR or JSON, and records the Accept header.
func cborAPIServer(t *testing.T, scheme *runtime.Scheme) (*httptest.Server, *[]string) {
	t.Helper()
	list := &catalogv1alpha1.EpisodeList{
		TypeMeta: metav1.TypeMeta{APIVersion: "catalog.clustarr.io/v1alpha1", Kind: "EpisodeList"},
		ListMeta: metav1.ListMeta{ResourceVersion: "7"},
		Items:    []catalogv1alpha1.Episode{*episode("media", "a", 1), *episode("media", "a", 2)},
	}
	body, err := runtime.Encode(cbor.NewSerializer(scheme, scheme), list)
	require.NoError(t, err)
	jsonBody, err := runtime.Encode(json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme, scheme, json.SerializerOptions{}), list)
	require.NoError(t, err)
	accepts := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*accepts = append(*accepts, r.Header.Get("Accept"))
		if strings.HasPrefix(r.Header.Get("Accept"), runtime.ContentTypeCBOR) {
			w.Header().Set("Content-Type", runtime.ContentTypeCBOR)
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", runtime.ContentTypeJSON)
		_, _ = w.Write(jsonBody)
	}))
	t.Cleanup(srv.Close)
	return srv, accepts
}

// controller-runtime's own client, on the clustarr fork, asks for CBOR
// first under the gates and decodes the CBOR answer into the kind; with
// ClientsPreferCBOR off it asks for JSON as before. This is the request
// every controller and informer in the manager makes.
func TestControllerRuntimesClientSpeaksCBORUnderTheGates(t *testing.T) {
	scheme := k8s.MustNewScheme()
	srv, accepts := cborAPIServer(t, scheme)
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(catalogv1alpha1.GroupVersion.WithKind("Episode"), meta.RESTScopeNamespace)

	c, err := client.New(&rest.Config{Host: srv.URL}, client.Options{Scheme: scheme, Mapper: mapper})
	require.NoError(t, err)
	var list catalogv1alpha1.EpisodeList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("media")))
	require.Len(t, list.Items, 2)
	assert.Equal(t, "a-s01e002", list.Items[1].Name)
	require.Len(t, *accepts, 1)
	assert.Equal(t, runtime.ContentTypeCBOR+", */*", (*accepts)[0], "CBOR first, anything else as the fallback")

	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.ClientsPreferCBOR, false)
	c, err = client.New(&rest.Config{Host: srv.URL}, client.Options{Scheme: scheme, Mapper: mapper})
	require.NoError(t, err)
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("media")))
	require.Len(t, *accepts, 2)
	assert.Equal(t, runtime.ContentTypeJSON+", */*", (*accepts)[1], "JSON when CBOR is not preferred")
}

// A watch streams as a CBOR sequence, which client-go decodes behind the
// gate into the kind's objects through the fork's codec factory: the path a
// watch-list's initial events take.
func TestTheWatchStreamsAsACBORSequence(t *testing.T) {
	scheme := k8s.MustNewScheme()
	eps := []*catalogv1alpha1.Episode{episode("media", "a", 1), episode("media", "a", 2)}
	for _, ep := range eps {
		ep.APIVersion, ep.Kind = "catalog.clustarr.io/v1alpha1", "Episode"
	}
	var accept, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept, query = r.Header.Get("Accept"), r.URL.RawQuery
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

	gv := catalogv1alpha1.GroupVersion
	cfg := &rest.Config{Host: srv.URL, GroupVersion: &gv, APIPath: "/apis"}
	// The fork's codec factory: client-go's helper adds the CBOR serializer
	// under ClientsAllowCBOR.
	cfg.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: rest.CodecFactoryForGeneratedClient(scheme, serializer.NewCodecFactory(scheme))}
	rc, err := rest.RESTClientFor(cfg)
	require.NoError(t, err)
	w, err := rc.Get().Resource("episodes").Namespace("media").VersionedParams(&metav1.ListOptions{Watch: true, ResourceVersion: "7"}, runtime.NewParameterCodec(scheme)).Watch(t.Context())
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
	assert.Equal(t, runtime.ContentTypeCBOR+", */*", accept)
	assert.Contains(t, query, "watch=true")
	assert.Contains(t, query, "resourceVersion=7")
}
