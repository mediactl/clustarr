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
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestARecreatedProbeBucketWakesEveryPendingProbe (F1.4): a recreated
// clustarr-probes lost every outstanding request, so S16's OnRecreated wakes
// the files whose status says a probe is pending -- in every namespace --
// to ask again, and no other.
func TestARecreatedProbeBucketWakesEveryPendingProbe(t *testing.T) {
	mf := func(ns, name string, status metav1.ConditionStatus, reason string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Status: catalogv1alpha1.MediaFileStatus{Conditions: []metav1.Condition{{
				Type: catalogv1alpha1.MediaFileConditionProbed, Status: status, Reason: reason, LastTransitionTime: metav1.Now(),
			}}},
		}
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		mf("media", "pending", metav1.ConditionFalse, catalogv1alpha1.MediaFileReasonProbePending),
		mf("anime", "pending-too", metav1.ConditionFalse, catalogv1alpha1.MediaFileReasonProbePending),
		mf("media", "failed", metav1.ConditionFalse, "ProbeFailed"),
		mf("media", "probed", metav1.ConditionTrue, "Probed"),
		&catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "never-planned"}},
	).Build()
	var got []remediation.Key
	wakePendingProbes(context.Background(), c, func(k remediation.Key) { got = append(got, k) })
	assert.ElementsMatch(t, []remediation.Key{
		{Kind: remediation.KindMediaFile, Namespace: "media", Name: "pending"},
		{Kind: remediation.KindMediaFile, Namespace: "anime", Name: "pending-too"},
	}, got)
}
