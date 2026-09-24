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
	"net/http"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// handleIndexerDownload is "GET /{indexer}/download": resolves a release
// payload through Config.Download -- clustarr.rpc.indexarr.download's own
// body -- using the indexer's own session cookies/passkeys.
func (s *Server) handleIndexerDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := r.PathValue("indexer")
	idx, err := s.resolveIndexer(ctx, name)
	if err != nil {
		s.writeLookupError(ctx, w, name, err)
		return
	}
	if !indexerEnabled(idx) {
		http.Error(w, "facade: no such indexer", http.StatusNotFound)
		return
	}

	guid := r.URL.Query().Get("guid")
	if guid == "" {
		s.writeTorznabError(w, http.StatusBadRequest, torznab.ErrMissingParameter, "guid is required")
		return
	}

	dlCtx, cancel := context.WithTimeout(ctx, s.cfg.SearchTimeout)
	defer cancel()
	resp := s.cfg.Download(dlCtx, schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: idx.Namespace, Name: idx.Name},
		GUID:       guid,
		URL:        r.URL.Query().Get("url"),
	})

	switch {
	case resp.Error != "":
		// DownloadResponse.Error is intentionally NOT echoed to the caller
		// verbatim on other verbs in this package (writeLookupError), but
		// this one is: it is app/indexer/download's own designed error
		// contract for this exact caller-facing purpose ("DownloadResponse
		// carries exactly one of Bytes, MagnetURL or RedirectURL ... or an
		// Error and none" -- app/indexer/download/doc.go), not an internal
		// detail like an apiserver error.
		s.writeTorznabError(w, http.StatusBadGateway, torznab.ErrUnknown, resp.Error)
	case len(resp.Bytes) > 0:
		ct := resp.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(resp.Bytes)
	case resp.MagnetURL != "":
		http.Redirect(w, r, resp.MagnetURL, http.StatusFound)
	case resp.RedirectURL != "":
		http.Redirect(w, r, resp.RedirectURL, http.StatusFound)
	default:
		s.writeTorznabError(w, http.StatusBadGateway, torznab.ErrUnknown, "indexer returned no payload")
	}
}
