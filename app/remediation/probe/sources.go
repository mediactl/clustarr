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

package probe

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// Sources: S16 (answered probe records, and every file whose status says a
// probe is pending when the bucket was recreated, §4.9) and S23's
// TranscodeJob and AudioGraft rows, as the old controller watched them,
// until F6 and F7 fold the kinds.
func (a *Adapter) Sources(mgr ctrl.Manager) ([]remediation.Source, error) {
	c := mgr.GetClient()
	return []remediation.Source{
		{Name: "S16/clustarr-probes", Raw: recordsource.New(a.o.Bus, events.BucketProbes, remediation.FileKey,
			recordsource.OnRecreated(func(ctx context.Context, enqueue func(remediation.Key)) { wakePendingProbes(ctx, c, enqueue) }))},
		{
			Name: "S23/TranscodeJob", Object: &transcodev1alpha1.TranscodeJob{}, Handler: remediation.MapFiles(mediafile.FileOfTranscodeJob),
			Predicates: []predicate.Predicate{mediafile.TranscodeJobPhaseChanged()},
		},
		{
			Name: "S23/AudioGraft", Object: &transcodev1alpha1.AudioGraft{}, Handler: remediation.MapFiles(mediafile.FileOfAudioGraft),
			Predicates: []predicate.Predicate{mediafile.AudioGraftDoneChanged()},
		},
	}, nil
}

// wakePendingProbes enqueues every MediaFile whose status says a probe is
// pending (Probed False, ProbePending): its request went with a recreated
// bucket (loop spec §4.9, §5.15 "records bucket lost").
func wakePendingProbes(ctx context.Context, c client.Reader, enqueue func(remediation.Key)) {
	var list catalogv1alpha1.MediaFileList
	if err := c.List(ctx, &list, client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Warn("remediation: could not list MediaFiles to wake their pending probes", "error", err)
		return
	}
	for i := range list.Items {
		mf := &list.Items[i]
		cond := k8s.FindCondition(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionProbed)
		if cond != nil && cond.Status == metav1.ConditionFalse && cond.Reason == catalogv1alpha1.MediaFileReasonProbePending {
			enqueue(remediation.Key{Kind: remediation.KindMediaFile, Namespace: mf.Namespace, Name: mf.Name})
		}
	}
}
