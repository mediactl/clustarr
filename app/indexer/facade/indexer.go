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

package facade

import (
	"context"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
)

// errIndexerNotFound is resolveIndexer's own "no such indexer" for the
// cluster-wide List path, where there is no apierrors.NewNotFound to return
// (List never 404s; the miss is this package's own conclusion, not the
// apiserver's). writeLookupError treats it exactly like apierrors.IsNotFound.
var errIndexerNotFound = errors.New("facade: no such indexer")

// resolveIndexer looks up the Indexer named by a /{indexer}/... path
// segment.
//
// With Config.Namespace set, this is a single Get. Empty, it falls back to a
// cluster-wide List filtered by name and errors on more than one match --
// Torznab's own URL shape has no room for a namespace segment (§6.2 spells
// the route "/{indexer}/api", not "/{namespace}/{indexer}/api"), so an
// unconfigured Namespace can only ever guess, and refusing to guess between
// two same-named Indexers in different namespaces is safer than serving
// whichever List happened to return first.
func (s *Server) resolveIndexer(ctx context.Context, name string) (*indexv1alpha1.Indexer, error) {
	if s.cfg.Namespace != "" {
		idx := &indexv1alpha1.Indexer{}
		if err := s.cfg.Client.Get(ctx, client.ObjectKey{Namespace: s.cfg.Namespace, Name: name}, idx); err != nil {
			return nil, err
		}
		return idx, nil
	}

	var list indexv1alpha1.IndexerList
	if err := s.cfg.Client.List(ctx, &list); err != nil {
		return nil, err
	}
	var found *indexv1alpha1.Indexer
	for i := range list.Items {
		if list.Items[i].Name != name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf(
				"facade: indexer %q is ambiguous across namespaces (at least %q and %q); set Config.Namespace",
				name, found.Namespace, list.Items[i].Namespace)
		}
		found = &list.Items[i]
	}
	if found == nil {
		return nil, errIndexerNotFound
	}
	return found, nil
}

// indexerEnabled reports spec.enabled -- the operator's master on/off
// switch, distinct from EnableAutomaticSearch/EnableInteractiveSearch, which
// app/indexer/search's own candidate selection already applies and which
// legitimately degrade to a zero-result search rather than a facade-level
// rejection.
func indexerEnabled(idx *indexv1alpha1.Indexer) bool {
	return idx.Spec.Enabled == nil || *idx.Spec.Enabled
}
