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

package actions_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// unmonitoredSeries has a specials season off, season 1 off by override
// and season 2 known only from its status.
func unmonitoredSeries() *catalogv1alpha1.Series {
	return &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "bb", ResourceVersion: "41"},
		Spec: catalogv1alpha1.SeriesSpec{
			Monitored: new(false),
			Seasons:   []catalogv1alpha1.SeasonSpec{{Number: 0, Monitored: new(false)}, {Number: 1, Monitored: new(false)}},
		},
		Status: catalogv1alpha1.SeriesStatus{Seasons: []catalogv1alpha1.SeasonStatus{{Number: 0}, {Number: 1}, {Number: 2}}},
	}
}

// A series switched to monitored monitors every season but the specials
// (owner, 2026-10-27), in one merge patch of the whole seasons list under
// the read resourceVersion, the specials' entry as it was.
func TestMonitorSeriesMonitorsEverySeasonButTheSpecials(t *testing.T) {
	w := &fakeWriter{}
	_, err := actions.MonitorSeries(t.Context(), w, unmonitoredSeries())
	require.NoError(t, err)
	require.Len(t, w.patches, 1)
	p := w.patches[0]
	require.IsType(t, &catalogv1alpha1.Series{}, p.obj)
	require.Equal(t, types.MergePatchType, p.patchType)
	require.JSONEq(t, `{"metadata":{"resourceVersion":"41"},"spec":{"monitored":true,"seasons":[
		{"number":0,"monitored":false},{"number":1,"monitored":true},{"number":2,"monitored":true}]}}`, string(p.data))
	require.Equal(t, actions.FieldManager, p.opts.FieldManager)

	t.Run("no specials entry is added", func(t *testing.T) {
		s := unmonitoredSeries()
		s.Spec.Seasons = nil
		got := actions.AllSeasonsMonitored(s)
		require.Equal(t, []catalogv1alpha1.SeasonSpec{{Number: 1, Monitored: new(true)}, {Number: 2, Monitored: new(true)}}, got)
	})
}

// The Edit dialog's seasons go out under the series' resourceVersion, and
// only for a series.
func TestEditItemSendsSeasonsUnderTheReadResourceVersion(t *testing.T) {
	w := &fakeWriter{}
	s := unmonitoredSeries()
	_, err := actions.EditItem(t.Context(), w, "media", commonv1.MediaKindSeries, "bb", actions.EditRequest{
		QualityProfileRef: "hd", Monitored: new(true),
		Seasons: actions.AllSeasonsMonitored(s), ResourceVersion: s.ResourceVersion,
	})
	require.NoError(t, err)
	require.Len(t, w.patches, 1)
	require.JSONEq(t, `{"metadata":{"resourceVersion":"41"},"spec":{"qualityProfileRef":"hd","monitored":true,"seasons":[
		{"number":0,"monitored":false},{"number":1,"monitored":true},{"number":2,"monitored":true}]}}`, string(w.patches[0].data))

	_, err = actions.EditItem(t.Context(), w, "media", commonv1.MediaKindMovie, "heat", actions.EditRequest{
		QualityProfileRef: "hd", Seasons: actions.AllSeasonsMonitored(s), ResourceVersion: "7",
	})
	require.ErrorIs(t, err, actions.ErrInvalid)
}
