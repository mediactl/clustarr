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

package replay

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/app/catalog/history/replay"
	"github.com/mediactl/clustarr/app/remediation"
)

// Actuator is the replay actuator.
type Actuator struct{ r *replay.Replayer }

// New is the replay actuator over r.
func New(r *replay.Replayer) *Actuator { return &Actuator{r: r} }

// Name implements remediation.Actuator.
func (*Actuator) Name() string { return "replay" }

// Act hands applied's metadata to the Replayer when it carries the replay
// annotation: it republishes the dead letter and consumes the annotation
// with its own guarded JSON patch (history/replay).
func (a *Actuator) Act(ctx context.Context, _ *remediation.Env, _ client.Client, applied *catalogv1alpha1.MediaFile) (time.Duration, error) {
	if _, ok := applied.Annotations[history.AnnotationReplay]; !ok {
		return 0, nil
	}
	obj := &metav1.PartialObjectMetadata{
		TypeMeta:   metav1.TypeMeta{APIVersion: catalogv1alpha1.GroupVersion.String(), Kind: "MediaFile"},
		ObjectMeta: *applied.ObjectMeta.DeepCopy(),
	}
	res, err := a.r.Replay(ctx, obj)
	return res.RequeueAfter, err
}
