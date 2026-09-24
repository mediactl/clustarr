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

package download

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// EngineDownloader is a definition-backed Indexer's client as this package
// sees it: Cardigann's Engine.Download bound to that Indexer's definition,
// configuration and session, plus the secret values a diagnostic must never
// carry. app/indexer/controller/indexer's Cardigann client implements it.
type EngineDownloader interface {
	Download(ctx context.Context, link string) (io.ReadCloser, error)
	Secrets() []string
}

// EngineFetcher adapts an EngineDownloader to [Fetcher], so a
// definition-backed release goes through exactly the classification a
// Torznab release does -- the payload cap, the HTML-page refusal, the
// content-type sniff, the grab accounting -- with only the fetch itself
// replaced by the definition's download block.
func EngineFetcher(d EngineDownloader) Fetcher {
	return &engineFetcher{d: d, scrub: scrubber(d.Secrets())}
}

type engineFetcher struct {
	d     EngineDownloader
	scrub func(string) string
}

func (f *engineFetcher) Scrub(s string) string { return f.scrub(s) }

// Fetch runs Engine.Download. The engine returns either the payload bytes or,
// when the definition resolved the release to a magnet (a magnet link, a
// selector that matched one, or an infohash block), the magnet URI itself as
// the body -- which is reported as MagnetURL, never as a .torrent payload.
func (f *engineFetcher) Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	ctx, span := tracing.Start(ctx, "indexarr.download.engine")
	defer span.End()

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("app/indexer/download: parse download URL: %w", cardigann.RedactErr(err))
	}
	if u.Scheme == "magnet" {
		return &FetchResult{MagnetURL: rawURL, FinalURL: u}, nil
	}

	rc, err := f.d.Download(ctx, rawURL)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, fmt.Errorf("app/indexer/download: definition download from %s: %w",
			cardigann.RedactURL(u), cardigann.RedactErr(err))
	}
	defer func() { _ = rc.Close() }()
	// The engine already buffers under its own 8 MiB cap; this re-reads
	// under THIS package's payload cap so the broker budget holds.
	body, err := readPayload(rc)
	if err != nil {
		return nil, err
	}
	if trimmed := bytes.TrimSpace(body); strings.HasPrefix(string(trimmed), "magnet:") {
		return &FetchResult{MagnetURL: string(trimmed), FinalURL: u}, nil
	}
	return &FetchResult{
		Status:     http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(bytes.NewReader(body)),
		ContentLen: int64(len(body)),
		FinalURL:   u,
	}, nil
}

// definitionBacked reports whether idx is driven by a Cardigann definition,
// and so must be fetched through Service.Definitions.
func definitionBacked(idx *indexv1alpha1.Indexer) bool {
	return idx.Spec.Definition != nil || idx.Spec.DefinitionRef != nil
}
