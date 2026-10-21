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

package e2e

// Scenario 19 (Live TV spec 2026-10-07): an IPTVProvider over the in-cluster
// iptv-stub becomes two blocks, each its own HDHomeRun surface and XEPG
// guide on its own port; two viewers of one channel share one upstream, and
// with the provider's one tuner in use a second channel is refused.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
)

// fixtureIPTVStubService is config/e2e/iptv-stub.yaml's Service.
const fixtureIPTVStubService = "iptv-stub"

type lineupEntry struct {
	GuideNumber, GuideName, URL string
}

// getBody GETs url, with Host set when host is not empty.
func getBody(ctx context.Context, t *testing.T, url, host string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	if host != "" {
		req.Host = host
	}
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	require.NoError(t, err)
	return res.StatusCode, string(b)
}

func TestLiveTVLineupGuideAndASharedStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureIPTVStubService)
	name := uniqueName("livetv")
	one := int32(1)
	creds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-credentials", Namespace: Namespace},
		Data:       map[string][]byte{"username": []byte("e2e"), "password": []byte("secret")},
	}
	require.NoError(t, k8sClient.Create(ctx, creds))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), creds) })
	p := &clustarrv1.IPTVProvider{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace},
		Spec: clustarrv1.IPTVProviderSpec{
			Server:         "http://iptv-stub." + Namespace + ".svc",
			CredentialsRef: corev1.LocalObjectReference{Name: creds.Name},
			DVR:            clustarrv1.DVRSpec{MaxChannels: &one}, // two channels, so two blocks
			Filters:        []clustarrv1.ChannelFilter{{Name: "e2e", Type: clustarrv1.FilterGroupTitle, Match: "E2E"}},
		},
	}
	require.NoError(t, k8sClient.Create(ctx, p))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), p) })
	for i, key := range []string{"t:e2e.1", "t:e2e.2"} {
		ch := &clustarrv1.IPTVChannel{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", name, i), Namespace: Namespace},
			Spec:       clustarrv1.IPTVChannelSpec{ProviderRef: name, Key: key, Number: fmt.Sprintf("%d", 1001+i), Active: true},
		}
		require.NoError(t, k8sClient.Create(ctx, ch))
		cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), ch) })
	}
	var got clustarrv1.IPTVProvider
	waitFor(t, ctx, 5*time.Minute, "the provider is Ready with two active channels in two blocks", func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &got); err != nil {
			return false, err
		}
		return meta.IsStatusConditionTrue(got.Status.Conditions, clustarrv1.ConditionReady) &&
			got.Status.Lineup != nil && got.Status.Lineup.Active == 2 && len(got.Status.Blocks) == 2, nil
	})

	blocks := map[string]clustarrv1.BlockStatus{}
	for _, b := range got.Status.Blocks {
		blocks[b.Start] = b
	}
	require.Contains(t, blocks, "1001")
	require.Contains(t, blocks, "1002")
	assert.NotEqual(t, blocks["1001"].Port, blocks["1002"].Port, "a port per block")
	assert.NotEqual(t, blocks["1001"].DeviceID, blocks["1002"].DeviceID, "a device ID per block")
	assert.NotZero(t, blocks["1001"].Port)
	assert.Equal(t, "/livetv/"+Namespace+"/"+name+"/devices/"+blocks["1001"].DeviceID, blocks["1001"].Endpoint)
	require.NotNil(t, got.Status.Account)
	assert.Equal(t, "Active", got.Status.Account.Status)
	require.NotNil(t, got.Status.Tuners)
	assert.Equal(t, int32(1), got.Status.Tuners.Total, "the account's max_connections")

	base, stop := portForwardService(ctx, t, "livetv", 80)
	defer stop()
	prefix := base + "/livetv/" + Namespace + "/" + name

	// Each block's lineup at its device endpoint, which cluster-plex's proxy
	// on the block's port sends every request below; and a range of the
	// whole lineup.
	code, body := getBody(ctx, t, base+blocks["1001"].Endpoint+"/lineup.json", "")
	require.Equal(t, http.StatusOK, code, body)
	var lineup []lineupEntry
	require.NoError(t, json.Unmarshal([]byte(body), &lineup))
	require.Len(t, lineup, 1)
	assert.Equal(t, "1001", lineup[0].GuideNumber)
	code, body = getBody(ctx, t, prefix+"/lineup.json?from=1002", "")
	require.Equal(t, http.StatusOK, code, body)
	lineup = nil
	require.NoError(t, json.Unmarshal([]byte(body), &lineup))
	require.Len(t, lineup, 1)
	assert.Equal(t, "1002", lineup[0].GuideNumber)

	// Each block's guide holds its channels only; the provider's holds both.
	code, body = getBody(ctx, t, base+blocks["1002"].Endpoint+"/xmltv.xml", "")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `<channel id="1002">`)
	assert.NotContains(t, body, `<channel id="1001">`)
	code, body = getBody(ctx, t, prefix+"/xmltv.xml", "")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `<channel id="1001">`)
	assert.Contains(t, body, `<channel id="1002">`)

	// Two viewers of 1001 hold one upstream, and the provider's one tuner.
	read, release := make(chan error, 2), make(chan struct{})
	var viewers sync.WaitGroup
	for range 2 {
		viewers.Add(1)
		go func() {
			defer viewers.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+blocks["1001"].Endpoint+"/stream/1001", nil)
			if err != nil {
				read <- err
				return
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				read <- err
				return
			}
			defer func() { _ = res.Body.Close() }()
			if res.StatusCode != http.StatusOK {
				read <- fmt.Errorf("stream: %s", res.Status)
				return
			}
			_, err = io.CopyN(io.Discard, res.Body, 1<<20)
			read <- err
			<-release
		}()
	}
	defer func() { close(release); viewers.Wait() }()
	for range 2 {
		require.NoError(t, <-read, "each viewer reads 1 MiB")
	}

	stub, stopStub := portForwardService(ctx, t, fixtureIPTVStubService, 80)
	defer stopStub()
	code, body = getBody(ctx, t, stub+"/stats", "")
	require.Equal(t, http.StatusOK, code)
	var stats struct {
		Connections map[string]int `json:"connections"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &stats))
	assert.Equal(t, 1, stats.Connections["1"], "two viewers, one upstream")

	code, _ = getBody(ctx, t, base+blocks["1002"].Endpoint+"/stream/1002", "")
	assert.Equal(t, http.StatusServiceUnavailable, code, "the one tuner is in use")
}
