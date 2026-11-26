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

// Package librarycli is what the manager's and the agents' commands share
// to reach the library store (ADR-0024): its address and an agent's
// read-only wiring. It is apart from pkg/k8s so that package,
// which every reconciler links, links no database driver.
package librarycli

import (
	"context"
	"errors"
	"os"
	"strings"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/library"
	"github.com/mediactl/clustarr/pkg/library/ctrlwire"
)

// Address is the library store's address: --library-dsn, else
// $CLUSTARR_LIBRARY_DSN. The address carries a password, so the flag has no
// default: one would print in --help.
func Address(o k8s.Options) string {
	if dsn := strings.TrimSpace(o.LibraryDSN); dsn != "" {
		return dsn
	}
	return strings.TrimSpace(os.Getenv(library.EnvDSN))
}

// Open connects to the library store. Migrating is the manager's: it alone
// creates the schema; every other process opens what it made.
func Open(ctx context.Context, o k8s.Options, lo library.Options) (*library.Store, error) {
	dsn := Address(o)
	if dsn == "" {
		return nil, errors.New("the library store has no address: set --library-dsn or $" + library.EnvDSN)
	}
	return library.Open(ctx, dsn, lo)
}

// WireReadOnly points an agent's manager at the library store: its REST
// mapper, its cache's informers, and a client that reads the library kinds
// there and refuses every write. The agents' ReadOnly wrapper goes outside
// it, so a write is refused and counted as a write to any kind is. The
// manager and the ui, which write the store, wire pkg/library/readwrite
// instead: no agent links that package.
func WireReadOnly(opts *ctrl.Options, store *library.Store) (*library.Wiring, error) {
	// AgentManagerOptions set the read-only constructor; it is rebuilt
	// below around the library's client.
	opts.NewClient = nil
	// Every clustarr cache strips managedFields (ManagerOptions'
	// CacheTransform), so the store never reads them for one.
	w := &library.Wiring{Store: store, Scheme: opts.Scheme, OmitManagedFields: true}
	if err := ctrlwire.Apply(w, opts); err != nil {
		return nil, err
	}
	inner := opts.NewClient
	opts.NewClient = func(cfg *rest.Config, o client.Options) (client.Client, error) {
		c, err := inner(cfg, o)
		if err != nil {
			return nil, err
		}
		return k8s.ReadOnly(c), nil
	}
	return w, nil
}
