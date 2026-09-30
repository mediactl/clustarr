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

package rescan

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A series the scan finds on disk starts with nothing monitored: its files
// import regardless, and the owner turns on the seasons they want searched.
// Before, it took the CRD's old default, every episode, and a library of 147
// shows queued 5,718 episode searches.
func TestApplySeriesMonitorsNothing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	w := &Worker{Client: c}
	st := &scanState{
		scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "scan"}},
		root: &catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "shows"}},
	}
	require.NoError(t, w.applySeries(context.Background(), st, SeriesCandidate{
		Name: "andor-393189", TvdbID: 393189, Folder: "Andor (2022)", QualityProfileRef: "hd",
	}))
	var s catalogv1alpha1.Series
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "tv", Name: "andor-393189"}, &s))
	assert.Equal(t, catalogv1alpha1.SeriesMonitorNone, s.Spec.AddOptions.Monitor)
}
