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

package k8s_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func TestApplyConfigurationsFromCarriesEveryLeaf(t *testing.T) {
	in := []catalogv1alpha1.IndexerOutcome{
		{Name: "a", State: catalogv1alpha1.IndexerOutcomeOK, Count: 3, DurationMs: 120, Error: "e"},
		{Name: "b", State: catalogv1alpha1.IndexerOutcomeError},
	}
	got, err := k8s.ApplyConfigurationsFrom[catalogac.IndexerOutcomeApplyConfiguration](in)
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, catalogac.IndexerOutcome().
		WithName("a").WithState(catalogv1alpha1.IndexerOutcomeOK).
		WithCount(3).WithDurationMs(120).WithError("e"), got[0])

	// A zero leaf the API type tags omitempty stays absent rather than
	// becoming an explicit zero that would claim the leaf under SSA.
	assert.Equal(t, ptr.To("b"), got[1].Name)
	assert.Nil(t, got[1].Count, "a zero omitempty leaf must stay unset")
	assert.Nil(t, got[1].Error)

	// Byte-exact with marshalling the API value itself.
	for i := range in {
		want, err := json.Marshal(in[i])
		require.NoError(t, err)
		have, err := json.Marshal(got[i])
		require.NoError(t, err)
		assert.JSONEq(t, string(want), string(have))
	}
}

func TestApplyConfigurationsFromEmpty(t *testing.T) {
	got, err := k8s.ApplyConfigurationsFrom[catalogac.IndexerOutcomeApplyConfiguration]([]catalogv1alpha1.IndexerOutcome(nil))
	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}
