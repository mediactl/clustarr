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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
)

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
