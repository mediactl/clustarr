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

package artwork_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func override(t catalogv1alpha1.ImageType, url string) catalogv1alpha1.ArtworkOverride {
	return catalogv1alpha1.ArtworkOverride{Type: t, URL: url}
}

func entry(t catalogv1alpha1.ImageType, src catalogv1alpha1.ArtworkSource, url string) catalogv1alpha1.ArtworkEntry {
	return catalogv1alpha1.ArtworkEntry{Type: t, Source: src, SourceURL: url, Digest: "d", SizeBytes: 1}
}

func TestDrift(t *testing.T) {
	const (
		poster = catalogv1alpha1.ImageTypePoster
		fanart = catalogv1alpha1.ImageTypeFanart
		custom = catalogv1alpha1.ArtworkSourceCustom
		prov   = catalogv1alpha1.ArtworkSourceProvider
	)
	for _, tc := range []struct {
		name      string
		overrides []catalogv1alpha1.ArtworkOverride
		entries   []catalogv1alpha1.ArtworkEntry
		drifted   bool
	}{
		{name: "nothing at all", drifted: false},
		{name: "provider entries only", entries: []catalogv1alpha1.ArtworkEntry{entry(poster, prov, "https://p")}, drifted: false},
		{name: "an override with no entry yet", overrides: []catalogv1alpha1.ArtworkOverride{override(poster, "https://mine")}, drifted: true},
		{
			name:      "an override already stored",
			overrides: []catalogv1alpha1.ArtworkOverride{override(poster, "https://mine")},
			entries:   []catalogv1alpha1.ArtworkEntry{entry(poster, custom, "https://mine")},
			drifted:   false,
		},
		{
			name:      "an override whose URL changed",
			overrides: []catalogv1alpha1.ArtworkOverride{override(poster, "https://mine-v2")},
			entries:   []catalogv1alpha1.ArtworkEntry{entry(poster, custom, "https://mine")},
			drifted:   true,
		},
		{
			name:      "an override over a provider entry",
			overrides: []catalogv1alpha1.ArtworkOverride{override(poster, "https://mine")},
			entries:   []catalogv1alpha1.ArtworkEntry{entry(poster, prov, "https://p")},
			drifted:   true,
		},
		{
			name:    "a custom entry whose override was removed",
			entries: []catalogv1alpha1.ArtworkEntry{entry(poster, prov, "https://p"), entry(fanart, custom, "https://mine")},
			drifted: true,
		},
		{
			name:      "a custom entry whose override remains, beside a removed one",
			overrides: []catalogv1alpha1.ArtworkOverride{override(poster, "https://mine")},
			entries:   []catalogv1alpha1.ArtworkEntry{entry(poster, custom, "https://mine"), entry(fanart, custom, "https://gone")},
			drifted:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash, drifted := artwork.Drift(tc.overrides, tc.entries)
			assert.Equal(t, tc.drifted, drifted)
			assert.Len(t, hash, 64, "a hex SHA-256")
		})
	}
}

func TestDriftHashIsOrderIndependentAndMovesWithEveryURL(t *testing.T) {
	a := override(catalogv1alpha1.ImageTypePoster, "https://a")
	b := override(catalogv1alpha1.ImageTypeFanart, "https://b")
	h1, _ := artwork.Drift([]catalogv1alpha1.ArtworkOverride{a, b}, nil)
	h2, _ := artwork.Drift([]catalogv1alpha1.ArtworkOverride{b, a}, nil)
	assert.Equal(t, h1, h2, "spec.artwork is hashed sorted by type")

	b2 := override(catalogv1alpha1.ImageTypeFanart, "https://b2")
	h3, _ := artwork.Drift([]catalogv1alpha1.ArtworkOverride{a, b2}, nil)
	assert.NotEqual(t, h1, h3)

	moved := override(catalogv1alpha1.ImageTypeBanner, "https://b")
	h4, _ := artwork.Drift([]catalogv1alpha1.ArtworkOverride{a, moved}, nil)
	assert.NotEqual(t, h1, h4, "the same URL under another type is another spec")

	e1, _ := artwork.Drift(nil, nil)
	e2, _ := artwork.Drift([]catalogv1alpha1.ArtworkOverride{}, nil)
	assert.Equal(t, e1, e2)
}

// fetchCollector subscribes to every artwork-fetch subject on a membus and
// records each delivered envelope by subject.
type fetchCollector struct {
	mu   sync.Mutex
	subj map[string][]*events.Envelope
}

func collectFetches(t *testing.T, ctx context.Context, bus events.Bus) *fetchCollector {
	t.Helper()
	c := &fetchCollector{subj: map[string][]*events.Envelope{}}
	stop, err := bus.Subscribe(ctx, events.Subscription{
		Stream:  events.StreamWorkCatalogarr,
		Durable: "test-artwork-fetch-collector",
		Filters: []string{events.FilterCatalogArtworkFetch},
	}, func(_ context.Context, m events.Message) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.subj[m.Subject()] = append(c.subj[m.Subject()], m.Envelope())
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return c
}

func (c *fetchCollector) on(subject string) []*events.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*events.Envelope(nil), c.subj[subject]...)
}

func (c *fetchCollector) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, envs := range c.subj {
		n += len(envs)
	}
	return n
}

func TestPublishFetchPublishesOnceWhenDrifted(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	got := collectFetches(t, ctx, bus)

	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "films", UID: types.UID("uid-heat")},
		Spec: catalogv1alpha1.MovieSpec{Artwork: []catalogv1alpha1.ArtworkOverride{
			override(catalogv1alpha1.ImageTypePoster, "https://example.org/heat.jpg"),
		}},
	}
	for range 3 { // a hot reconcile loop
		require.NoError(t, artwork.PublishFetch(ctx, bus, m, commonv1.MediaKindMovie))
	}

	subject := events.WorkArtworkFetchSubject(events.MediaKey(string(commonv1.MediaKindMovie), "films", "heat"))
	require.Eventually(t, func() bool { return len(got.on(subject)) >= 1 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	envs := got.on(subject)
	require.Len(t, envs, 1, "the Msg-Id absorbs the repeats")

	hash, drifted := artwork.Drift(m.Spec.Artwork, nil)
	require.True(t, drifted)
	assert.Equal(t, schema.MsgIDForArtworkFetch(m.UID, hash), envs[0].ID)
	assert.Equal(t, "films/heat", envs[0].Key, "the <namespace>/<name> routing key every worker parses")
	var task schema.ArtworkFetchTask
	require.NoError(t, schema.Decode(envs[0].Schema, envs[0].Data, &task))
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, task.MediaRef)
}

func TestPublishFetchPublishesNothingWithoutDrift(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	got := collectFetches(t, ctx, bus)

	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "lost", Namespace: "tv", UID: types.UID("uid-lost")},
		Spec: catalogv1alpha1.SeriesSpec{Artwork: []catalogv1alpha1.ArtworkOverride{
			override(catalogv1alpha1.ImageTypePoster, "https://example.org/lost.jpg"),
		}},
		Status: catalogv1alpha1.SeriesStatus{Artwork: []catalogv1alpha1.ArtworkEntry{
			entry(catalogv1alpha1.ImageTypePoster, catalogv1alpha1.ArtworkSourceCustom, "https://example.org/lost.jpg"),
		}},
	}
	require.NoError(t, artwork.PublishFetch(ctx, bus, s, commonv1.MediaKindSeries))
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, got.total())
}

func TestPublishFetchRefusesAKindThatIsNotTheObjects(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	m := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "films", UID: "u"}}
	require.Error(t, artwork.PublishFetch(ctx, bus, m, commonv1.MediaKindSeries))
	require.Error(t, artwork.PublishFetch(ctx, bus, &catalogv1alpha1.Episode{}, commonv1.MediaKindEpisode),
		"an Episode has no artwork of its own")
}

// Every manager's cache strips managedFields (pkg/k8s.ManagerOptions), so an
// item read through one looks as if the gateway owned nothing: extracting
// from it would apply artwork alone and release all of status.metadata.
func TestExtractGatewayStatusRefusesAnObjectReadWithoutManagedFields(t *testing.T) {
	stripped := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "films", UID: "u"},
		Status:     catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{Title: "Heat"}},
	}
	_, err := artwork.ExtractGatewayStatus(stripped, nil)
	require.ErrorIs(t, err, artwork.ErrNoManagedFields)
}

func TestPassWithoutAReaderPanicsWithAClearMessage(t *testing.T) {
	assert.PanicsWithValue(t,
		"artwork: Pass.Reader is required -- pass the uncached API reader (mgr.GetAPIReader()); "+
			"the manager's cache lags this gateway's own writes and strips managedFields",
		func() {
			_ = artwork.Pass{}.Run(context.Background(), types.NamespacedName{Namespace: "n", Name: "x"},
				commonv1.MediaKindMovie, nil, artwork.ExtractGatewayStatus)
		})
}
