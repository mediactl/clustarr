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

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A folder whose TheTVDB id is excluded -- a series deleted with its files
// kept and "Add import list exclusion" ticked -- is recorded unmatched and
// no Series is created.
func TestCreateSeriesSkipsAnExcludedFolder(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	data, err := events.ExclusionEntry{Namespace: "tv", Name: "andor", Kind: "series"}.Encode()
	require.NoError(t, err)
	_, err = bus.KV(events.BucketImportExclusions).Put(ctx, events.ExclusionKey(catalogv1alpha1.ExclusionIDKeyTVDB, "393189"), data)
	require.NoError(t, err)

	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	w := &Worker{Client: c, Bus: bus}
	st := &scanState{
		scan: &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "scan"}},
		root: &catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Namespace: "tv", Name: "shows"},
			Spec: catalogv1alpha1.RootFolderSpec{Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd"}}},
	}
	got, err := w.createSeries(ctx, st, "Andor (2022) {tvdb-393189}/S01E01.mkv", &SeriesCandidate{TvdbID: 393189, Title: "Andor"})
	require.NoError(t, err)
	assert.Nil(t, got)
	var list catalogv1alpha1.SeriesList
	require.NoError(t, c.List(ctx, &list))
	assert.Empty(t, list.Items)
	require.Len(t, st.progress.Unmatched, 1)
	assert.Contains(t, st.progress.Unmatched[0].Reason, "excluded")
}
