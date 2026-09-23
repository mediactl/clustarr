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
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// writeTorznabError writes a standalone <error> document -- the exact inverse
// of torznab.ParseError, and the shape every Torznab/Newznab client already
// knows how to read on a non-2xx (or, per the spec, even a 200) reply.
func (s *Server) writeTorznabError(w http.ResponseWriter, status int, code torznab.ErrorCode, description string) {
	w.Header().Set("Content-Type", torznabContentType)
	w.WriteHeader(status)
	_ = torznab.WriteError(w, &torznab.Error{Code: code, Description: description, HTTPStatus: status})
}

// writeCaps writes c as a t=caps document.
func (s *Server) writeCaps(ctx context.Context, w http.ResponseWriter, c torznab.Caps) {
	w.Header().Set("Content-Type", torznabContentType)
	if err := torznab.WriteCaps(w, c); err != nil {
		// The header is already flushed by the time WriteCaps can fail
		// (a client that hung up mid-write, most often), so there is
		// nothing left to tell the caller; log it server-side only.
		logging.FromContext(ctx).Debug("facade: writing caps failed", "err", err)
	}
}

// writeLookupError maps a resolveIndexer failure onto an HTTP response:
// "no such indexer" (a routing-shaped 404, no XML body -- see doc.go on why
// an unknown {indexer} segment is not treated as a Torznab function error)
// for anything resolveIndexer classifies as not-found, and a generic 500
// for everything else, WITHOUT echoing the underlying error to the caller --
// it is logged instead, so a cluster-internal detail (an apiserver error, an
// ambiguous-namespace message naming other namespaces) never reaches
// whatever is on the other end of this port.
func (s *Server) writeLookupError(ctx context.Context, w http.ResponseWriter, name string, err error) {
	if apierrors.IsNotFound(err) || errors.Is(err, errIndexerNotFound) {
		http.Error(w, "facade: no such indexer", http.StatusNotFound)
		return
	}
	logging.FromContext(ctx).Error("facade: indexer lookup failed", "indexer", name, "err", err)
	http.Error(w, "facade: indexer lookup failed", http.StatusInternalServerError)
}
