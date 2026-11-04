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
	"context"
	"fmt"
	"net/http"
	"os"
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/runtime/serializer/cbor"
	"k8s.io/apimachinery/pkg/watch"
	clientfeatures "k8s.io/client-go/features"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
)

// The four large kinds stream from the apiserver in CBOR (owner,
// 2026-11-04: "Enable ClientsAllowCBOR and ClientsPreferCBOR"). talos's
// apiserver serves it (CBORServingAndStorage is on), and client-go 0.37
// decodes it behind its ClientsAllowCBOR gate; controller-runtime 0.25,
// though, builds the codec factories of its client and its cache without
// the CBOR serializer, so neither gate reaches its own clients, and
// ClientsPreferCBOR would make every typed write ask for an encoder that is
// not there, failing it before it was sent, with no 415 to fall back on.
// So: the row cache builds its own lister-watcher for each kind it holds,
// over a REST client whose codec factory carries the CBOR serializer and
// whose content types ask for CBOR (cborListerWatcher), which is where the
// bytes are -- the 44,000-object initial list and the watch -- and
// ClientsAllowCBOR is on by default in this process (init), which only adds
// client-go's CBOR stream decoder and scrubs nothing from a JSON client.
// ClientsPreferCBOR stays as the environment says, off, until
// controller-runtime carries the serializer. KUBE_FEATURE_ClientsAllowCBOR
// in the environment overrides the default; with the gate off client-go
// rewrites the row cache's CBOR content types to JSON itself.

// envAllowCBOR is client-go's environment variable for the gate.
const envAllowCBOR = "KUBE_FEATURE_ClientsAllowCBOR"

// init turns ClientsAllowCBOR on through client-go's own gates (a value set
// this way outranks the environment, so it is set only when the environment
// does not name the gate), which keeps client-go's test helper and the
// environment's say working as they do.
func init() {
	if _, ok := os.LookupEnv(envAllowCBOR); ok {
		return
	}
	if s, ok := clientfeatures.FeatureGates().(interface {
		Set(clientfeatures.Feature, bool) error
	}); ok {
		_ = s.Set(clientfeatures.ClientsAllowCBOR, true)
	}
}

// cborAllowed says whether client-go will send the row cache's CBOR
// content types as they are.
func cborAllowed() bool {
	return clientfeatures.FeatureGates().Enabled(clientfeatures.ClientsAllowCBOR)
}

// cborCodecs is a codec factory for scheme that carries the CBOR
// serializer beside JSON's.
func cborCodecs(scheme *runtime.Scheme) serializer.CodecFactory {
	return serializer.NewCodecFactory(scheme, serializer.WithSerializer(cbor.NewSerializerInfo))
}

// cborListerWatcher lists and watches k over CBOR: the shape of
// controller-runtime's own structured lister-watcher (its cache's
// makeListWatcher), on a REST client that asks for CBOR and can decode it.
// namespace restricts it, or "" for every namespace. The reflector sets
// the watch's own options (a watch-list's initial events, bookmarks).
func cborListerWatcher(cfg *rest.Config, httpClient *http.Client, scheme *runtime.Scheme, k *kind, namespace string) (toolscache.ListerWatcher, error) {
	gv := k.gvk.GroupVersion()
	c := rest.CopyConfig(cfg)
	c.GroupVersion = &gv
	c.APIPath = "/apis"
	if c.UserAgent == "" {
		c.UserAgent = rest.DefaultKubernetesUserAgent()
	}
	c.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: cborCodecs(scheme)}
	c.ContentType = runtime.ContentTypeCBOR
	c.AcceptContentTypes = runtime.ContentTypeCBOR + "," + runtime.ContentTypeJSON
	if httpClient == nil {
		var err error
		if httpClient, err = rest.HTTPClientFor(c); err != nil {
			return nil, fmt.Errorf("rowcache: %s: http client: %w", k.gvk.Kind, err)
		}
	}
	client, err := rest.RESTClientForConfigAndClient(c, httpClient)
	if err != nil {
		return nil, fmt.Errorf("rowcache: %s: rest client: %w", k.gvk.Kind, err)
	}
	listGVK := gv.WithKind(k.gvk.Kind + "List")
	listObj, err := scheme.New(listGVK)
	if err != nil {
		return nil, fmt.Errorf("rowcache: %s: %w", k.gvk.Kind, err)
	}
	paramCodec := runtime.NewParameterCodec(scheme)
	request := func(opts metav1.ListOptions) *rest.Request {
		req := client.Get().Resource(k.resource).VersionedParams(&opts, paramCodec)
		if namespace != "" {
			req.Namespace(namespace)
		}
		return req
	}
	return &toolscache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, opts metav1.ListOptions) (runtime.Object, error) {
			res := listObj.DeepCopyObject()
			if err := request(opts).Do(ctx).Into(res); err != nil {
				return nil, err
			}
			return res, nil
		},
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			opts.Watch = true
			return request(opts).Watch(ctx)
		},
	}, nil
}

// cborNamespace is the one namespace the cache is restricted to, "" for
// all, and false when the cache's options give k a shape the row cache's
// own lister-watcher does not reproduce (several namespaces, a default
// selector, a ByObject entry), in which case the kind keeps
// controller-runtime's lister-watcher and JSON.
func cborNamespace(opts cache.Options, k *kind) (string, bool) {
	if opts.DefaultLabelSelector != nil || opts.DefaultFieldSelector != nil {
		return "", false
	}
	for obj := range opts.ByObject {
		if reflect.TypeOf(obj) == reflect.TypeOf(k.obj) {
			return "", false
		}
	}
	switch len(opts.DefaultNamespaces) {
	case 0:
		return "", true
	case 1:
		for ns := range opts.DefaultNamespaces {
			if ns == cache.AllNamespaces {
				return "", true
			}
			return ns, true
		}
	}
	return "", false
}
