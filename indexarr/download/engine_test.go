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
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

type stubEngine struct {
	body    string
	err     error
	secrets []string
	calls   int
}

func (s *stubEngine) Download(context.Context, string) (io.ReadCloser, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return io.NopCloser(strings.NewReader(s.body)), nil
}
func (s *stubEngine) Secrets() []string { return s.secrets }

func TestEngineFetcherReportsAResolvedMagnetAsAMagnet(t *testing.T) {
	f := EngineFetcher(&stubEngine{body: "magnet:?xt=urn:btih:ABC&dn=x\n"})
	res, err := f.Fetch(context.Background(), "https://tr.example/details/1")
	require.NoError(t, err)
	require.Equal(t, "magnet:?xt=urn:btih:ABC&dn=x", res.MagnetURL)
	require.Nil(t, res.Body)
}

func TestEngineFetcherHandsBackThePayload(t *testing.T) {
	f := EngineFetcher(&stubEngine{body: "d8:announce0:e"})
	res, err := f.Fetch(context.Background(), "https://tr.example/details/1")
	require.NoError(t, err)
	require.Equal(t, 200, res.Status)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, "d8:announce0:e", string(body))
	require.Equal(t, int64(len(body)), res.ContentLen)
}

// A magnet link is the payload already; the engine is not even asked.
func TestEngineFetcherNeverRunsTheEngineForAMagnetLink(t *testing.T) {
	eng := &stubEngine{}
	res, err := EngineFetcher(eng).Fetch(context.Background(), "magnet:?xt=urn:btih:ABC")
	require.NoError(t, err)
	require.Equal(t, "magnet:?xt=urn:btih:ABC", res.MagnetURL)
	require.Zero(t, eng.calls)
}

func TestEngineFetcherScrubsTheIndexersSecrets(t *testing.T) {
	f := EngineFetcher(&stubEngine{secrets: []string{"passkey-0123456789"}})
	require.Equal(t, "echoed *** back", f.Scrub("echoed passkey-0123456789 back"))
}

func definitionIndexer() *indexv1alpha1.Indexer {
	idx := testIndexer("media", "leetx", "uid-def", indexv1alpha1.LimitUnitDay)
	idx.Spec.DefinitionRef = ptr.To("1337x")
	return idx
}

// rpc.indexarr.download dispatches a definition-backed Indexer to the
// Cardigann path, and never to the plain GET, which would skip the
// definition's download block and hand grabarr a details page.
func TestADefinitionBackedGrabDispatchesToTheEngine(t *testing.T) {
	ctx := context.Background()
	idx := definitionIndexer()
	req := schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: "media", Name: "leetx"}, GUID: "g",
		URL: "https://tr.example/torrent/1/",
	}
	plain := func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
		t.Fatal("a definition-backed grab went through the plain fetcher")
		return nil, nil
	}
	eng := &stubEngine{body: "magnet:?xt=urn:btih:DEF"}
	s := &Service{
		Client: fakeClient(t, idx),
		Fetch:  plain,
		Definitions: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			return EngineFetcher(eng), nil
		},
	}
	got := s.Handle(ctx, req)
	require.Empty(t, got.Error)
	require.Equal(t, "magnet:?xt=urn:btih:DEF", got.MagnetURL)
	require.Equal(t, 1, eng.calls)

	// Unwired, it REFUSES rather than falling back to the plain GET.
	s.Definitions = nil
	got = s.Handle(ctx, req)
	require.Contains(t, got.Error, "not configured")
}

func TestAnEngineFailureIsScrubbed(t *testing.T) {
	idx := definitionIndexer()
	eng := &stubEngine{
		err:     errors.New("tracker said passkey-0123456789 is banned"),
		secrets: []string{"passkey-0123456789"},
	}
	s := &Service{
		Client: fakeClient(t, idx),
		Fetch:  nilFetcherFor,
		Definitions: func(context.Context, *indexv1alpha1.Indexer) (Fetcher, error) {
			return EngineFetcher(eng), nil
		},
	}
	got := s.Handle(context.Background(), schema.DownloadRequest{
		IndexerRef: schema.Ref{Namespace: "media", Name: "leetx"}, GUID: "g", URL: "https://tr.example/t/1",
	})
	require.NotEmpty(t, got.Error)
	require.NotContains(t, got.Error, "passkey-0123456789")
}
