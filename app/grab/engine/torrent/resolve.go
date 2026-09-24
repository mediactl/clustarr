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

package torrent

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

// maxPayloadBytes caps a fetched .torrent body, the same convention every
// other HTTP client in this tree uses (CLAUDE.md: "every HTTP response body
// is read through a cap"). A real .torrent is kilobytes; anything reaching
// this floor is not one.
const maxPayloadBytes = 8 << 20 // 8MiB

// ErrResponseTooLarge is returned when a fetched body exceeds
// [maxPayloadBytes].
var ErrResponseTooLarge = errors.New("torrent: response body exceeds size limit")

// IndexerResolver is the narrow client this package needs against
// clustarr.rpc.indexarr.download (app/indexer/download, already served -- see
// its own doc comment for the payload contract this mirrors). It exists,
// rather than a bare events.Requester, so tests can inject
// [FakeIndexerResolver] instead of standing up a bus.
type IndexerResolver interface {
	ResolveDownload(ctx context.Context, req schema.DownloadRequest) (schema.DownloadResponse, error)
}

type busIndexerResolver struct{ r events.Requester }

// NewBusIndexerResolver wraps a bus's request/reply half as an
// [IndexerResolver].
func NewBusIndexerResolver(r events.Requester) IndexerResolver { return &busIndexerResolver{r: r} }

func (b *busIndexerResolver) ResolveDownload(ctx context.Context, req schema.DownloadRequest) (schema.DownloadResponse, error) {
	var resp schema.DownloadResponse
	if err := b.r.Request(ctx, events.RPCIndexDownload, req, &resp); err != nil {
		return schema.DownloadResponse{}, fmt.Errorf("torrent: download RPC: %w", err)
	}
	return resp, nil
}

// FakeIndexerResolver is an [IndexerResolver] test double: it returns
// Response/Err unconditionally and records every request it saw.
type FakeIndexerResolver struct {
	Response schema.DownloadResponse
	Err      error

	Requests []schema.DownloadRequest
}

func (f *FakeIndexerResolver) ResolveDownload(_ context.Context, req schema.DownloadRequest) (schema.DownloadResponse, error) {
	f.Requests = append(f.Requests, req)
	if f.Err != nil {
		return schema.DownloadResponse{}, f.Err
	}
	return f.Response, nil
}

// resolved is what [resolveSource] produces: exactly one of Magnet and
// Payload is set, mirroring [download.AddRequest]'s own "exactly one" rule.
type resolved struct {
	Magnet           string
	Payload          []byte
	ExpectedInfoHash string
}

// resolveSource turns a Download's spec.source into transfer bytes or a
// magnet URI. namespace is the Download's own namespace, which
// IndexerDownload.IndexerRef is documented to share.
//
// httpClient is used for the two branches that need to fetch a URL directly
// (torrentURL, and indexerDownload's RedirectURL branch) -- spec §4.4's own
// words for IndexerDownload: "the payload is addressable by URL, or
// indexerDownload names the release and grabarr resolves it through
// indexarr". No indexer credentials are attached to either fetch: torrentURL
// is documented as "needs no indexer credentials", and a RedirectURL from
// indexarr's download RPC is, per that package's own doc comment, "where to
// fetch the payload, when the indexer will not proxy it" -- indexarr already
// held the session and chose not to use it for this link.
func resolveSource(ctx context.Context, httpClient *http.Client, resolver IndexerResolver, namespace string, src downloadv1alpha1.DownloadSource) (resolved, error) {
	var out resolved
	if src.ExpectedInfoHash != nil {
		out.ExpectedInfoHash = *src.ExpectedInfoHash
	}

	switch {
	case src.MagnetURL != nil:
		out.Magnet = *src.MagnetURL
		return out, nil

	case src.TorrentURL != nil:
		payload, err := fetchURL(ctx, httpClient, *src.TorrentURL)
		if err != nil {
			return resolved{}, fmt.Errorf("torrent: fetch torrentURL: %w", err)
		}
		out.Payload = payload
		return out, nil

	case src.IndexerDownload != nil:
		if resolver == nil {
			return resolved{}, errors.New("torrent: spec.source.indexerDownload set but no IndexerResolver configured")
		}
		resp, err := resolver.ResolveDownload(ctx, schema.DownloadRequest{
			IndexerRef: schema.Ref{Namespace: namespace, Name: src.IndexerDownload.IndexerRef},
			GUID:       src.IndexerDownload.GUID,
			URL:        src.IndexerDownload.URL,
		})
		if err != nil {
			return resolved{}, fmt.Errorf("torrent: resolve indexerDownload: %w", err)
		}
		if resp.Error != "" {
			return resolved{}, fmt.Errorf("torrent: indexer resolve failed: %s", resp.Error)
		}
		switch {
		case len(resp.Bytes) > 0:
			out.Payload = resp.Bytes
			return out, nil
		case resp.MagnetURL != "":
			out.Magnet = resp.MagnetURL
			return out, nil
		case resp.RedirectURL != "":
			payload, err := fetchURL(ctx, httpClient, resp.RedirectURL)
			if err != nil {
				return resolved{}, fmt.Errorf("torrent: fetch redirectURL: %w", err)
			}
			out.Payload = payload
			return out, nil
		default:
			return resolved{}, errors.New("torrent: indexer resolve returned no bytes, magnet or redirect")
		}

	default:
		// NZBURL is the fourth DownloadSource member; a protocol=torrent
		// Download's XValidation rule (exactly one of the four) never lets it
		// coexist with a torrent-shaped source in practice, but this branch is
		// unreachable-by-construction rather than assumed: it is reached only
		// if every pointer above is nil, which DownloadSource's own CEL rule
		// forbids for any object that reached the apiserver.
		return resolved{}, errors.New("torrent: spec.source has no torrent-compatible member set")
	}
}

// fetchURL GETs url with ctx's deadline and caps the body at
// [maxPayloadBytes], the same io.LimitReader(body, max+1) pattern
// pkg/torznab and pkg/cardigann use.
func fetchURL(ctx context.Context, httpClient *http.Client, url string) ([]byte, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("torrent: build request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("torrent: fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torrent: fetch %s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("torrent: read body: %w", err)
	}
	if len(body) > maxPayloadBytes {
		return nil, fmt.Errorf("%w: at least %d bytes", ErrResponseTooLarge, len(body))
	}
	return body, nil
}
