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
