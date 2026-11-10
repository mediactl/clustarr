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

package audiobook_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
)

// The add options read as applied from the first pass on, whatever they
// say and whether or not they were applied before.
func TestApplyAddOptionsRecordsTheFlagOnEveryPass(t *testing.T) {
	for _, m := range []*catalogv1alpha1.Audiobook{
		{},
		{Spec: catalogv1alpha1.AudiobookSpec{AddOptions: catalogv1alpha1.AudiobookAddOptions{SearchForMissing: ptr.To(false)}}},
		{Status: catalogv1alpha1.AudiobookStatus{AddOptionsApplied: true}},
	} {
		ac := audiobook.ApplyAddOptions(catalogac.AudiobookStatus(), m)
		require.NotNil(t, ac.AddOptionsApplied)
		assert.True(t, *ac.AddOptionsApplied)
	}
}

// SearchOnAdd: searchForMissing on (its default) or off, whatever
// addOptionsApplied says -- the search stage reads it for the audiobook's
// first automatic search, which may come passes after the options were
// recorded as applied.
func TestSearchOnAdd(t *testing.T) {
	assert.True(t, audiobook.SearchOnAdd(&catalogv1alpha1.Audiobook{}), "the default is to search on add")
	assert.True(t, audiobook.SearchOnAdd(&catalogv1alpha1.Audiobook{
		Spec: catalogv1alpha1.AudiobookSpec{AddOptions: catalogv1alpha1.AudiobookAddOptions{SearchForMissing: ptr.To(true)}},
	}))
	assert.False(t, audiobook.SearchOnAdd(&catalogv1alpha1.Audiobook{
		Spec: catalogv1alpha1.AudiobookSpec{AddOptions: catalogv1alpha1.AudiobookAddOptions{SearchForMissing: ptr.To(false)}},
	}), "turned off")
	assert.False(t, audiobook.SearchOnAdd(&catalogv1alpha1.Audiobook{
		Spec:   catalogv1alpha1.AudiobookSpec{AddOptions: catalogv1alpha1.AudiobookAddOptions{SearchForMissing: ptr.To(false)}},
		Status: catalogv1alpha1.AudiobookStatus{AddOptionsApplied: true},
	}), "still off once the options are recorded as applied")
}
