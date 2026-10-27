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

package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// AllSeasonsMonitored is series' spec.seasons with every season but the
// specials monitored (owner, 2026-10-27: a monitored series monitors all of
// its seasons, specials excluded): every season status.seasons or
// spec.seasons names, the specials' own entry kept as it is and none added
// for them. The Series controller applies each changed season to its
// episodes once (series.SeasonCascade), as it does a season toggle.
func AllSeasonsMonitored(series *catalogv1alpha1.Series) []catalogv1alpha1.SeasonSpec {
	byNumber := map[int32]catalogv1alpha1.SeasonSpec{}
	for _, s := range series.Spec.Seasons {
		byNumber[s.Number] = s
	}
	for _, s := range series.Status.Seasons {
		if _, ok := byNumber[s.Number]; !ok && s.Number != 0 {
			byNumber[s.Number] = catalogv1alpha1.SeasonSpec{Number: s.Number}
		}
	}
	out := make([]catalogv1alpha1.SeasonSpec, 0, len(byNumber))
	for n, s := range byNumber {
		if n != 0 {
			s.Monitored = new(true)
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b catalogv1alpha1.SeasonSpec) int { return int(a.Number - b.Number) })
	return out
}

// monitorSeriesPatch is MonitorSeries' body: spec.seasons is a list, which
// a JSON merge patch replaces whole, so it is sent whole under the read
// resourceVersion.
type monitorSeriesPatch struct {
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Spec struct {
		Monitored bool                         `json:"monitored"`
		Seasons   []catalogv1alpha1.SeasonSpec `json:"seasons"`
	} `json:"spec"`
}

// MonitorSeries monitors series -- the Series as the caller read it -- and
// every one of its seasons but the specials (AllSeasonsMonitored), in one
// merge patch under the read resourceVersion, so a season edited meanwhile
// is a Conflict, never silently overwritten. It is the monitor toggle for a
// series that was unmonitored; one already monitored keeps its seasons as
// they are (SetMonitored).
func MonitorSeries(ctx context.Context, p Patcher, series *catalogv1alpha1.Series) (*catalogv1alpha1.Series, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.MonitorSeries")
	defer span.End()

	if series == nil || series.Namespace == "" || series.Name == "" {
		err := fmt.Errorf("%w: need the series as read", ErrInvalid)
		tracing.RecordError(span, err)
		return nil, err
	}
	var body monitorSeriesPatch
	body.Metadata.ResourceVersion = series.ResourceVersion
	body.Spec.Monitored = true
	body.Spec.Seasons = AllSeasonsMonitored(series)
	raw, err := json.Marshal(body)
	if err != nil {
		err = fmt.Errorf("actions: encode the monitor-series patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}
	patched := &catalogv1alpha1.Series{}
	patched.Namespace, patched.Name = series.Namespace, series.Name
	if err := p.Patch(ctx, patched, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: monitor series %s/%s and its seasons: %w", series.Namespace, series.Name, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: series monitored with its seasons",
		"namespace", series.Namespace, "item", series.Name, "seasons", len(body.Spec.Seasons))
	return patched, nil
}

// MonitorSeries is [MonitorSeries] over the Actions' writer.
func (a *Actions) MonitorSeries(ctx context.Context, series *catalogv1alpha1.Series) (*catalogv1alpha1.Series, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return MonitorSeries(ctx, a.w, series)
}
