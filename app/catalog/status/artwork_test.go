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

package status_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A field added to ArtworkEntry later must either reach ArtworkEntries and
// ArtworkEntryLeaves or fail here: an unsent leaf is a released leaf.
func TestArtworkEntriesSendsEveryLeaf(t *testing.T) {
	full := catalogv1alpha1.ArtworkEntry{
		Type: catalogv1alpha1.ImageTypePoster, Source: catalogv1alpha1.ArtworkSourceCustom,
		SourceURL: "https://example.org/p.jpg", Digest: "abc", SizeBytes: 42,
		UpdatedAt: metav1.NewTime(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)),
	}
	acs := status.ArtworkEntries([]catalogv1alpha1.ArtworkEntry{full})
	require.Len(t, acs, 1)

	v := reflect.ValueOf(acs[0]).Elem()
	for i := range v.NumField() {
		assert.False(t, v.Field(i).IsNil(), "ArtworkEntries does not send %s", v.Type().Field(i).Name)
	}

	raw, err := json.Marshal(acs[0])
	require.NoError(t, err)
	var sent map[string]any
	require.NoError(t, json.Unmarshal(raw, &sent))
	keys := make([]string, 0, len(sent))
	for k := range sent {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, status.ArtworkEntryLeaves, keys)

	typed := reflect.TypeOf(catalogv1alpha1.ArtworkEntry{})
	assert.Equal(t, typed.NumField(), len(status.ArtworkEntryLeaves), "ArtworkEntryLeaves restates every ArtworkEntry field")
}

func TestOwnedStatusPathsReadsLeavesPerManagerAndSubresource(t *testing.T) {
	fields := metav1.NewFieldsV1
	managed := []metav1.ManagedFieldsEntry{
		{
			Manager: string(k8s.ManagerCatalogarrMetadata), Subresource: "status",
			FieldsV1: fields(`{"f:status":{"f:artwork":{"k:{\"type\":\"poster\"}":{".":{},"f:digest":{},"f:type":{}}},` +
				`"f:metadata":{".":{},"f:title":{}}}}`),
		},
		{
			Manager: string(k8s.ManagerCatalogarr), Subresource: "status",
			FieldsV1: fields(`{"f:status":{"f:metadata":{"f:selectedReleaseID":{}},"f:phase":{}}}`),
		},
		{
			Manager: string(k8s.ManagerCatalogarrMetadata), Subresource: "",
			FieldsV1: fields(`{"f:spec":{"f:title":{}}}`),
		},
	}
	got, err := status.OwnedStatusPaths(managed, k8s.ManagerCatalogarrMetadata)
	require.NoError(t, err)
	assert.Equal(t, sets.New("artwork[type=poster].digest", "artwork[type=poster].type", "metadata.title"), got)
	assert.Equal(t, sets.New("artwork", "metadata"), status.TopLevel(got))

	other, err := status.OwnedStatusPaths(managed, k8s.ManagerCatalogarr)
	require.NoError(t, err)
	assert.Equal(t, sets.New("metadata.selectedReleaseID", "phase"), other)
}

// A field added to OverlayEntry later must either reach OverlayEntryAC and
// OverlayEntryLeaves or fail here: the renderer's apply is its complete
// declaration of status.overlay, so an unsent leaf is a released leaf.
func TestOverlayEntryACSendsEveryLeaf(t *testing.T) {
	full := catalogv1alpha1.OverlayEntry{
		ProfileRef: "critics", Digest: "abc", RenderedFrom: "def",
		UpdatedAt: metav1.NewTime(time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)),
	}
	ac := status.OverlayEntryAC(full)

	v := reflect.ValueOf(ac).Elem()
	for i := range v.NumField() {
		assert.False(t, v.Field(i).IsNil(), "OverlayEntryAC does not send %s", v.Type().Field(i).Name)
	}

	raw, err := json.Marshal(ac)
	require.NoError(t, err)
	var sent map[string]any
	require.NoError(t, json.Unmarshal(raw, &sent))
	keys := make([]string, 0, len(sent))
	for k := range sent {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, status.OverlayEntryLeaves, keys)

	typed := reflect.TypeOf(catalogv1alpha1.OverlayEntry{})
	assert.Equal(t, typed.NumField(), len(status.OverlayEntryLeaves), "OverlayEntryLeaves restates every OverlayEntry field")
}

// The two managers' top-level sets are disjoint: the split of spec §B.3.
func TestGatewayAndRendererFieldsAreDisjoint(t *testing.T) {
	assert.Empty(t, sets.New(status.GatewayFields...).Intersection(sets.New(status.RendererFields...)))
	assert.Equal(t, []string{"overlay"}, status.RendererFields)
	assert.Equal(t, k8s.ManagerCatalogarrArtwork, status.RendererManager)
}

// PatchOverlay refuses every manager but the renderer's before it touches
// the client, so a nil client proves no apply was attempted.
func TestPatchOverlayRefusesEveryOtherManager(t *testing.T) {
	m := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "films"}}
	for _, mgr := range k8s.FieldManagers() {
		if mgr == status.RendererManager {
			continue
		}
		err := status.PatchOverlay(context.Background(), nil, mgr, m, nil)
		require.Error(t, err, "%s was allowed to write status.overlay", mgr)
		assert.ErrorIs(t, err, status.ErrNotTheRenderer)
	}
}

// Only Movie and Series carry status.overlay (spec §B.6).
func TestPatchOverlayRefusesAKindWithoutAnOverlay(t *testing.T) {
	err := status.PatchOverlay(context.Background(), nil, status.RendererManager,
		&catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "music"}}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, status.ErrNoOverlay)
}
