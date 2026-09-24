//go:build e2e

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

// Scenario 10 (docs/superpowers/plans/2026-09-18-remaining-work.md):
// "Cardigann (M6). An IndexerDefinition from the bundled corpus against the
// fixture HTML tracker -> results -> grab through the Torznab facade; an
// IndexerProxy on the HTTP path."
//
// "the bundled corpus" is not shipped yet -- indexarr/controller/indexer/
// cardigann.go's resolveDefinition own doc comment says so plainly ("the
// bundled corpus is NOT shipped yet ... an id resolves only through an
// IndexerDefinition"). So this scenario uses spec.definitionRef against an
// IndexerDefinition whose spec.yaml IS one of the bundled definitions'
// source files, testdata/cardigann/login-form.yml -- the same definition
// test/fixtures/cardigannstub serves, chosen for the reasons that
// package's own doc comment gives (a real login step, no Go-template
// path-building to reimplement in a fixture).
//
// # What "grab through the Torznab facade" means here
//
// facade/convert.go's releaseToTorznab writes a release's DownloadURL/
// MagnetURL straight onto the wire, unrewritten: G1-2's facade does not
// proxy every download through itself, it only BROKERS one when asked
// (GET /{indexer}/download -- indexarr/download/fetch.go's NewFetcherFor
// attaches the Indexer's own stored session cookie before fetching). So
// this scenario's fixture (cardigannstub) makes that distinction provable
// instead of moot: its one search result's download link is a same-origin,
// SESSION-GATED path, not a magnet URI. Fetched directly it 401s; fetched
// through GET /{indexer}/download it succeeds, because indexarr attaches
// the login session an outside caller could never have. That is "grab
// through the Torznab facade" proven as a real HTTP round trip, not
// inferred from a Download object -- driving an actual grabarr Download
// to Imported is out of reach here for the same permanent, already
// documented reason nonvideo_test.go's package doc comment gives for
// scenario 11 (test/fixtures/seeder's content is never a media extension).
//
// Build-tagged e2e. Per the standing instruction, this suite is written and
// has never been run against a kind cluster.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// Fixture Service names this file expects config/e2e to deploy.
const (
	fixtureCardigannStubService = "cardigann-stub"
	fixtureHTTPProxyStubService = "httpproxy-stub"
)

// cardigannIndexerReadyTimeout covers a real login POST plus the caps
// resolution pass, generous margin over indexerReadyTimeout's own
// reasoning for the same shape of wait.
const cardigannIndexerReadyTimeout = 2 * time.Minute

// facadeAPIKeySecretName is indexarr.DefaultFacadeAPIKeySecret's value,
// restated here because test/e2e does not import indexarr (its own file
// scope is cmd/clustarr, config/, charts/, catalogarr/, importarr/, api/ --
// none of which this task may touch, and importing indexarr's own package
// only to read one string constant is not worth crossing that line for).
// indexarr/run.go pins the same value permanently: it is also the literal
// config/manager/indexarr.yaml uses for CLUSTARR_FACADE_API_KEY_SECRET's
// default and for the readOnly kubectl example in that manifest's own
// comment.
const facadeAPIKeySecretName = "indexarr-facade"

// readFacadeAPIKey polls for the indexarr-facade Secret (indexarr generates
// it on first start when absent, indexarr/facadekey.go's ensureFacadeAPIKeys)
// and returns its apikey field.
func readFacadeAPIKey(ctx context.Context, t *testing.T) string {
	t.Helper()
	var key string
	waitFor(t, ctx, cardigannIndexerReadyTimeout, "facade API-key Secret "+facadeAPIKeySecretName, func(ctx context.Context) (bool, error) {
		var sec corev1.Secret
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: Namespace, Name: facadeAPIKeySecretName}, &sec); err != nil {
			//nolint:nilerr // keep polling: indexarr generates this lazily on its first reconcile pass
			return false, nil
		}
		v, ok := sec.Data["apikey"]
		if !ok || len(v) == 0 {
			return false, nil
		}
		key = string(v)
		return true, nil
	})
	require.NotEmpty(t, key)
	return key
}

// TestCardigannIndexerLoginSearchFacadeAndProxy is scenario 10 in full.
func TestCardigannIndexerLoginSearchFacadeAndProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureCardigannStubService)
	requireFixtureService(ctx, t, fixtureHTTPProxyStubService)
	requireFixtureService(ctx, t, "indexarr")

	// --- The IndexerDefinition: testdata/cardigann/login-form.yml's own
	// text, verbatim -- the single source of truth
	// test/fixtures/cardigannstub's own doc comment names, never a copy.
	yamlBytes, err := os.ReadFile("../data/cardigann/login-form.yml")
	require.NoError(t, err)
	def := &indexv1alpha1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e10-def")},
		Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: string(yamlBytes)},
	}
	require.NoError(t, k8sClient.Create(ctx, def))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), def) })

	waitFor(t, ctx, cardigannIndexerReadyTimeout, "IndexerDefinition "+def.Name+" Valid", func(ctx context.Context) (bool, error) {
		var live indexv1alpha1.IndexerDefinition
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(def), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return isConditionTrue(live.Status.Conditions, indexv1alpha1.IndexerDefinitionConditionValid), nil
	})

	// --- The IndexerProxy: a plain HTTP forward proxy fronting httpproxy-stub.
	proxy := &indexv1alpha1.IndexerProxy{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e10-proxy"), Namespace: Namespace},
		Spec: indexv1alpha1.IndexerProxySpec{
			Type: indexv1alpha1.IndexerProxyTypeHTTP,
			Host: fixtureHTTPProxyStubService + "." + Namespace + ".svc",
			Port: 80,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, proxy))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), proxy) })

	// --- The Secret: cardigannstub's Username/Password, matching
	// config/e2e/cardigann-stub.yaml's cardigann-fixture-credentials
	// exactly -- created fresh here rather than assumed, so this test does
	// not depend on that manifest's Secret name surviving unchanged.
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e10-creds"), Namespace: Namespace},
		StringData: map[string]string{"username": "e2e-cardigann-user", "password": "e2e-cardigann-pass"},
	}
	require.NoError(t, k8sClient.Create(ctx, sec))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), sec) })

	// --- The Indexer, definition-backed, routed through the proxy.
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e10-cardigann"), Namespace: Namespace},
		Spec: indexv1alpha1.IndexerSpec{
			DefinitionRef: ptr.To(def.Name),
			BaseURL:       "http://" + fixtureCardigannStubService + "." + Namespace + ".svc",
			SecretRef:     &corev1.LocalObjectReference{Name: sec.Name},
			ProxyRef:      ptr.To(proxy.Name),
			RequestDelay:  &metav1.Duration{Duration: 100 * time.Millisecond},
			Priority:      25,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, idx))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), idx) })

	var liveIdx indexv1alpha1.Indexer
	waitFor(t, ctx, cardigannIndexerReadyTimeout, "Indexer "+idx.Name+" Ready and Authenticated", func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(idx), &liveIdx); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return isConditionTrue(liveIdx.Status.Conditions, indexv1alpha1.IndexerConditionReady) &&
			isConditionTrue(liveIdx.Status.Conditions, indexv1alpha1.IndexerConditionAuthenticated), nil
	}, func() string {
		return fmt.Sprintf("conditions=%+v", liveIdx.Status.Conditions)
	})
	require.Equal(t, commonv1.ProtocolTorrent, liveIdx.Status.Protocol, "login-form.yml's caps declare no protocol of its own; the Cardigann leg always resolves torrent (definitionProtocol)")
	require.NotNil(t, liveIdx.Status.Caps)

	t.Run("IndexerProxy on the HTTP path", func(t *testing.T) {
		proxyBase, _ := portForwardService(ctx, t, fixtureHTTPProxyStubService, 8080)
		httpClient := &http.Client{Timeout: 15 * time.Second}
		body, status, err := httpGetString(ctx, httpClient, proxyBase+"/_proxied")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		var proxied []string
		require.NoError(t, json.Unmarshal([]byte(body), &proxied))
		found := false
		for _, u := range proxied {
			if strings.Contains(u, fixtureCardigannStubService) {
				found = true
				break
			}
		}
		require.True(t, found, "httpproxy-stub's own request log %v carries no request to %s -- "+
			"the Indexer's login/search traffic did not route through spec.proxyRef", proxied, fixtureCardigannStubService)
	})

	t.Run("results and grab through the Torznab facade", func(t *testing.T) {
		apikey := readFacadeAPIKey(ctx, t)
		facadeBase, _ := portForwardService(ctx, t, "indexarr", 8080)
		httpClient := &http.Client{Timeout: 30 * time.Second}

		searchURL := facadeBase + "/" + idx.Name + "/api?" + url.Values{
			"t": {"search"}, "apikey": {apikey},
		}.Encode()
		var releases []torznab.Release
		waitFor(t, ctx, cardigannIndexerReadyTimeout, "facade search returns the fixture release", func(ctx context.Context) (bool, error) {
			body, status, err := httpGetString(ctx, httpClient, searchURL)
			if err != nil || status != http.StatusOK {
				//nolint:nilerr // keep polling; a port-forward hiccup is transient
				return false, nil
			}
			rels, perr := torznab.ParseResults(strings.NewReader(body))
			if perr != nil {
				return false, nil //nolint:nilerr // a transient truncated response is not fatal here
			}
			releases = rels
			return len(rels) == 1, nil
		})
		require.Len(t, releases, 1)
		rel := releases[0]
		require.Equal(t, "Clustarr.E2E.Fixture.Cardigann.S01E01.1080p.WEB-DL", rel.Title)
		require.NotEmpty(t, rel.Link, "the fixture's download field is a same-origin path, not a magnet URI")
		require.NotEmpty(t, rel.GUID)

		// The link is session-gated: fetched directly (no session cookie
		// carried at all, since this is a fresh http.Client), it 401s.
		_, directStatus, err := httpGetString(ctx, httpClient, rel.Link)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnauthorized, directStatus, "cardigannstub's download route must refuse an unauthenticated fetch")

		// Through the facade's own download verb, indexarr attaches the
		// Indexer's stored login session and the same URL succeeds.
		downloadURL := facadeBase + "/" + idx.Name + "/download?" + url.Values{
			"apikey": {apikey}, "guid": {rel.GUID}, "url": {rel.Link},
		}.Encode()
		body, status, err := httpGetString(ctx, httpClient, downloadURL)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status, "GET /%s/download must succeed once brokered through indexarr's own stored session: %s", idx.Name, body)
		require.Equal(t, "d8:e2e-fixture-cardigann-torrent-contentse", body,
			"the facade must return cardigannstub.TorrentBytes verbatim")
	})
}
