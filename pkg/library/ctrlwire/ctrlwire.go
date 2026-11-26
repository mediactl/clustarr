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

// Package ctrlwire points a controller-runtime manager at the library store
// (ADR-0024). It is apart from pkg/library because it names the manager's
// options, and the ui -- which reads the store through a cache of its own --
// links no manager.
package ctrlwire

import (
	"fmt"
	"net/http"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/mediactl/clustarr/pkg/library"
)

// Apply sets the wiring's three seams on a manager's options: the REST
// mapper, the cache's informers and the client. It wraps what the options
// already name (a client constructor, an informer constructor), so call it
// after they are set and before the options' cache constructor reads them;
// a cache constructor that builds its own informers for some kinds takes
// the wiring's Source itself (pkg/k8s/rowcache's Options.Source).
func Apply(w *library.Wiring, opts *ctrl.Options) error {
	if w.Store == nil || w.Scheme == nil {
		return fmt.Errorf("library: the wiring has no store or no scheme")
	}
	switch n := len(opts.Cache.DefaultNamespaces); {
	case n == 1:
		for ns := range opts.Cache.DefaultNamespaces {
			w.Namespace = ns
		}
	case n > 1:
		return fmt.Errorf("library: the cache watches %d namespaces; the library is read whole or from one", n)
	}
	schemas := w.Store.Schemas()

	mapper := opts.MapperProvider
	if mapper == nil {
		mapper = apiutil.NewDynamicRESTMapper
	}
	opts.MapperProvider = func(cfg *rest.Config, httpClient *http.Client) (meta.RESTMapper, error) {
		inner, err := mapper(cfg, httpClient)
		if err != nil {
			return nil, err
		}
		return library.NewRESTMapper(inner, schemas), nil
	}

	newClient := opts.NewClient
	if newClient == nil {
		newClient = client.New
	}
	owner := opts.Client.FieldOwner
	opts.NewClient = func(cfg *rest.Config, o client.Options) (client.Client, error) {
		inner, err := newClient(cfg, o)
		if err != nil {
			return nil, err
		}
		cached := o.Cache != nil && o.Cache.Reader != nil
		if w.Client != nil {
			return w.Client(inner, cached, owner), nil
		}
		return library.NewReadClient(inner, w.Store, cached), nil
	}

	w.Informers(&opts.Cache)
	return nil
}

// Manager is mgr with its API reader reading the library kinds from the
// store: a reader of the apiserver itself finds no such kind there.
func Manager(mgr ctrl.Manager, store *library.Store) ctrl.Manager {
	return manager{Manager: mgr, reader: library.NewReader(mgr.GetAPIReader(), store, mgr.GetScheme())}
}

type manager struct {
	ctrl.Manager
	reader client.Reader
}

func (m manager) GetAPIReader() client.Reader { return m.reader }
