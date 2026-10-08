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

package scanapply

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A series the scan finds on disk starts with nothing monitored: its files
// import regardless, and the owner turns on the seasons they want searched.
// Before, it took the CRD's old default, every episode, and a library of 147
// shows queued 5,718 episode searches. (A6.1: the scan applier creates it,
// no longer the rescan.)
func TestApplySeriesMonitorsNothing(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	a := &Applier{Client: c}
	require.NoError(t, a.applySeries(context.Background(), "tv", "andor-393189", 393189, "hd", "shows", "Andor (2022)"))
	var s catalogv1alpha1.Series
	require.NoError(t, c.Get(context.Background(), client.ObjectKey{Namespace: "tv", Name: "andor-393189"}, &s))
	assert.Equal(t, catalogv1alpha1.SeriesMonitorNone, s.Spec.AddOptions.Monitor)
}
