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

package usenet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// defaultMaxPayloadBytes caps one resolved .nzb body. Real posts are XML text
// a few hundred KB even for a multi-hour season pack; 32MiB is generous
// headroom without leaving the cap effectively unbounded.
const defaultMaxPayloadBytes = 32 << 20

// ErrUnsupportedSource is returned when a Download's spec.source carries no
// payload a usenet engine can fetch: a bare magnet or .torrent URL, or an
// indexer response that resolved to one. A usenet Download must resolve to
// .nzb bytes, directly or through indexarr.
var ErrUnsupportedSource = errors.New("usenetengine: download source has no usenet-fetchable payload")

// ErrPayloadTooLarge is returned when a fetched body exceeds the resolver's
// configured maximum.
var ErrPayloadTooLarge = errors.New("usenetengine: fetched payload exceeds the configured maximum")

// Resolver turns a Download's spec.source into the raw .nzb bytes
// [download.Client.Add] needs.
//
// DownloadSource's own doc comment is explicit that a .torrent or .nzb body
// is never embedded in the Download object: "Either the payload is
// addressable by URL, or indexerDownload names the release and grabarr
// resolves it through indexarr." NZBURL needs no indexer credentials and is
// fetched directly; IndexerDownload is resolved through
// events.RPCIndexDownload, which applies the indexer's own auth, rate limit
// and proxy on indexarr's side before any bytes reach this process.
type Resolver struct {
	// RPC is the bus's request/reply half, used for events.RPCIndexDownload.
	// A nil RPC makes every IndexerDownload source fail immediately rather
	// than block until a caller notices Resolve was never wired up.
	RPC events.Requester

	// HTTP fetches NZBURL directly and a RedirectURL an indexer hands back.
	// nil means http.DefaultClient.
	HTTP *http.Client

	// MaxBytes caps a fetched body. Zero means [defaultMaxPayloadBytes].
	MaxBytes int64
}

func (r *Resolver) httpClient() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return http.DefaultClient
}

func (r *Resolver) maxBytes() int64 {
	if r.MaxBytes > 0 {
		return r.MaxBytes
	}
	return defaultMaxPayloadBytes
}

// Resolve returns the .nzb bytes for src, a Download in namespace ns's
// spec.source.
func (r *Resolver) Resolve(ctx context.Context, ns string, src downloadv1alpha1.DownloadSource) ([]byte, error) {
	switch {
	case src.NZBURL != nil && *src.NZBURL != "":
		return r.fetchURL(ctx, *src.NZBURL)
	case src.IndexerDownload != nil:
		return r.resolveIndexer(ctx, ns, *src.IndexerDownload)
	default:
		return nil, fmt.Errorf("%w: %+v", ErrUnsupportedSource, src)
	}
}

// resolveIndexer calls events.RPCIndexDownload, the same subject and
// schema.DownloadRequest/DownloadResponse pair
// indexarr/search.Service.handleDownload serves -- verified against source,
// not assumed, the same way catalogarr/worker/search.busSearchRPC calls
// events.RPCIndexSearch.
func (r *Resolver) resolveIndexer(ctx context.Context, ns string, id downloadv1alpha1.IndexerDownload) ([]byte, error) {
	if r.RPC == nil {
		return nil, fmt.Errorf("usenetengine: no RPC requester configured to resolve indexer download %q/%q", id.IndexerRef, id.GUID)
	}
	req := schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: ns, Name: id.IndexerRef},
		GUID:       id.GUID,
		URL:        id.URL,
	}
	var resp schema.DownloadResponse
	if err := r.RPC.Request(ctx, events.RPCIndexDownload, req, &resp); err != nil {
		return nil, fmt.Errorf("usenetengine: download RPC for %s/%s: %w", id.IndexerRef, id.GUID, err)
	}
	switch {
	case resp.Error != "":
		return nil, fmt.Errorf("usenetengine: indexarr: %s", resp.Error)
	case len(resp.Bytes) > 0:
		return resp.Bytes, nil
	case resp.RedirectURL != "":
		return r.fetchURL(ctx, resp.RedirectURL)
	case resp.MagnetURL != "":
		return nil, fmt.Errorf("%w: indexer %s returned a magnet link for a usenet download", ErrUnsupportedSource, id.IndexerRef)
	default:
		return nil, fmt.Errorf("usenetengine: indexarr returned an empty response for %s/%s", id.IndexerRef, id.GUID)
	}
}

// fetchURL performs a direct, unauthenticated GET -- the shape DownloadSource
// promises for NZBURL and TorrentURL ("needs no indexer credentials") and for
// an indexer's own RedirectURL, which is indexarr choosing not to proxy the
// body itself. The body is read through a cap: a package-level max, an
// io.LimitReader(body, max+1) and an ErrPayloadTooLarge sentinel, the same
// shape every other capped HTTP read in this tree uses (pkg/torznab,
// pkg/cardigann, pkg/subtitles/providers).
func (r *Resolver) fetchURL(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("usenetengine: build request for %s: %w", rawURL, err)
	}
	resp, err := r.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("usenetengine: fetch %s: %w", rawURL, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usenetengine: fetch %s: status %d", rawURL, resp.StatusCode)
	}

	limit := r.maxBytes()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("usenetengine: read %s: %w", rawURL, err)
	}
	if int64(len(buf)) > limit {
		return nil, fmt.Errorf("%w: %s exceeds %d bytes", ErrPayloadTooLarge, rawURL, limit)
	}
	return buf, nil
}
