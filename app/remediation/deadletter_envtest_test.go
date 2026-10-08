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

package remediation_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The loop folds the DLQ projector's annotation into DeadLettered on the
// file (§2.8): present while the annotation is, gone when an operator
// removes it, and catalog's alone -- with no planner bound at all.
func TestADeadLetteredAnnotationFoldsIntoTheLoopsCondition(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "deadletter", "heat-abc", "/data/movies/heat.mkv")
	r := e.Reconciler(t)
	e.Pass(t, r, nn)
	require.Nil(t, k8s.FindCondition(e.Get(t, nn).Status.Conditions, k8s.ConditionDeadLettered), "setup")
	e.Annotate(t, nn, k8s.AnnotationDeadLettered, "clustarr.work.markers.normal.x@2026-10-06T12:00:00Z")
	e.Pass(t, r, nn)
	got := e.Get(t, nn)
	c := k8s.FindCondition(got.Status.Conditions, k8s.ConditionDeadLettered)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, k8s.ReasonDeadLettered, c.Reason)
	assert.Equal(t, time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), c.LastTransitionTime.UTC(), "dated from the dead letter")
	assert.Contains(t, c.Message, "clustarr.work.markers.normal.x")
	assert.ElementsMatch(t, []string{"catalog/status", "import-worker/", "remediationtest/"}, remediationtest.FieldManagerNames(got.ManagedFields))
	assert.True(t, remediationtest.ManagesField(got.ManagedFields, "catalog", "status"))
	e.Annotate(t, nn, k8s.AnnotationDeadLettered, "")
	e.Pass(t, r, nn)
	assert.Nil(t, k8s.FindCondition(e.Get(t, nn).Status.Conditions, k8s.ConditionDeadLettered))
}
