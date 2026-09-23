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
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Getter is the one read [SetSeasonMonitored] needs before it patches. A
// controller-runtime client.Client satisfies it.
type Getter interface {
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
}

// seasonsPatch is the merge patch SetSeasonMonitored sends: the whole
// spec.seasons list -- a JSON merge patch replaces a list, so one entry
// cannot be sent alone -- under the resourceVersion the list was read at,
// so a write that raced another change is refused rather than overwriting
// it.
type seasonsPatch struct {
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Spec struct {
		Seasons []catalogv1alpha1.SeasonSpec `json:"seasons"`
	} `json:"spec"`
}

// seasonRetries is how many times a conflicting patch is re-read and
// retried before the conflict is the caller's: once, per the spec.
const seasonRetries = 1

// SetSeasonMonitored sets one season's monitored override on a Series
// (spec 2026-09-23-library-page-design, actions): it reads the Series,
// sets or adds the entry for number in spec.seasons, keeps every other
// entry as it was, and sends the list as a merge patch under [FieldManager]
// with the resourceVersion it read. A conflict is re-read and retried once;
// a second conflict is returned. The returned Series is the object the
// apiserver replied with, so a caller can render the value just written.
func SetSeasonMonitored(
	ctx context.Context, g Getter, p Patcher, namespace, series string, number int32, monitored bool,
) (*catalogv1alpha1.Series, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SetSeasonMonitored")
	defer span.End()

	if namespace == "" || series == "" || number < 0 {
		err := fmt.Errorf("%w: need a namespace, a series and a season number (got %q/%q season %d)",
			ErrInvalid, namespace, series, number)
		tracing.RecordError(span, err)
		return nil, err
	}

	var err error
	for attempt := 0; attempt <= seasonRetries; attempt++ {
		var current catalogv1alpha1.Series
		if err = g.Get(ctx, client.ObjectKey{Namespace: namespace, Name: series}, &current); err != nil {
			err = fmt.Errorf("actions: read series %s/%s before setting season %d: %w", namespace, series, number, err)
			break
		}
		var body seasonsPatch
		body.Metadata.ResourceVersion = current.ResourceVersion
		body.Spec.Seasons = withSeason(current.Spec.Seasons, number, monitored)
		raw, merr := json.Marshal(body)
		if merr != nil {
			err = fmt.Errorf("actions: encode spec.seasons patch: %w", merr)
			break
		}
		obj := &catalogv1alpha1.Series{}
		obj.SetNamespace(namespace)
		obj.SetName(series)
		err = p.Patch(ctx, obj, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager))
		if err == nil {
			logging.FromContext(ctx).Info("ui action: season monitored set",
				"namespace", namespace, "series", series, "season", number, "monitored", monitored, "attempt", attempt)
			return obj, nil
		}
		if !apierrors.IsConflict(err) {
			err = fmt.Errorf("actions: set season %d monitored=%t on series %s/%s: %w", number, monitored, namespace, series, err)
			break
		}
		// Somebody wrote the Series between the read and the patch: the
		// next attempt reads their version and puts this change on top.
	}
	tracing.RecordError(span, err)
	return nil, err
}

// withSeason is seasons with number's override set to monitored -- the
// existing entry changed, or one appended -- sorted by number, every other
// entry untouched.
func withSeason(seasons []catalogv1alpha1.SeasonSpec, number int32, monitored bool) []catalogv1alpha1.SeasonSpec {
	out := make([]catalogv1alpha1.SeasonSpec, 0, len(seasons)+1)
	found := false
	for _, s := range seasons {
		if s.Number == number {
			s.Monitored = ptr.To(monitored)
			found = true
		}
		out = append(out, s)
	}
	if !found {
		out = append(out, catalogv1alpha1.SeasonSpec{Number: number, Monitored: ptr.To(monitored)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

// SetSeasonMonitored is [SetSeasonMonitored] over the Actions' own client,
// which reads as well as writes (a controller-runtime client does); an
// Actions with no client, or one that cannot read, answers ErrNoWriter.
func (a *Actions) SetSeasonMonitored(
	ctx context.Context, namespace, series string, number int32, monitored bool,
) (*catalogv1alpha1.Series, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	g, ok := a.w.(Getter)
	if !ok {
		return nil, ErrNoWriter
	}
	return SetSeasonMonitored(ctx, g, a.w, namespace, series, number, monitored)
}
