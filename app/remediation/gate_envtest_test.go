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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestNoPlannerPlansFromAPendingProbe is §3.5's structural gate, W4.14's
// replacement: while the draft's probe is pending, no planner after the
// probe plans (its block stays as it was); a planner whose Gather the gate
// skipped because the stored probe was pending plans one second after the
// probe lands (Again), from inputs it gathered then.
func TestNoPlannerPlansFromAPendingProbe(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "gate", "heat-abc", "/data/movies/heat.mkv")
	state := "current"
	probe := remediationtest.ProbeFake(func(v *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		out.ProbeHash, out.MediaInfo = "h1", &commonv1.MediaInfo{VideoCodec: "h264"}
		if state == "pending" {
			k8s.MarkFalse(v.File, &out.Conditions, catalogv1alpha1.MediaFileConditionProbed, catalogv1alpha1.MediaFileReasonProbePending, "probe requested")
		} else {
			k8s.MarkTrue(v.File, &out.Conditions, catalogv1alpha1.MediaFileConditionProbed, "Probed", "probed")
		}
		return remediation.Result{}, nil
	})
	path := "/data/movies/a.mkv"
	nam := remediationtest.NamingFake(func(_ *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		out.Naming = &catalogv1alpha1.NamingStatus{ExpectedPath: path}
		return remediation.Result{}, nil
	})
	r := e.Reconciler(t, remediation.Bind[int](probe), remediation.Bind[int](nam))

	// A new file: the stored probe is absent, so naming gathers nothing
	// until the pass after the first probe lands.
	res := e.Pass(t, r, nn)
	assert.Nil(t, e.Get(t, nn).Status.Naming, "naming planned before the file was ever probed")
	assert.Zero(t, nam.Gathers.Load())
	assert.Equal(t, time.Second, res.RequeueAfter, "Again")
	e.Pass(t, r, nn)
	require.NotNil(t, e.Get(t, nn).Status.Naming)
	require.Equal(t, "/data/movies/a.mkv", e.Get(t, nn).Status.Naming.ExpectedPath, "setup")

	state, path = "pending", "/data/movies/b.mkv"
	plans := nam.Plans.Load()
	e.Pass(t, r, nn) // the stored probe is current, this pass's draft is pending
	e.Pass(t, r, nn) // the stored probe is pending too
	assert.Equal(t, plans, nam.Plans.Load(), "a planner after the probe planned from a pending probe")
	assert.Equal(t, "/data/movies/a.mkv", e.Get(t, nn).Status.Naming.ExpectedPath, "the block moved")
	gathers := nam.Gathers.Load()

	state = "current"
	res = e.Pass(t, r, nn) // the probe lands: naming gathered nothing this pass
	assert.Equal(t, gathers, nam.Gathers.Load(), "Gather ran while the stored probe was pending")
	assert.Equal(t, plans, nam.Plans.Load())
	assert.Positive(t, res.RequeueAfter)
	assert.LessOrEqual(t, res.RequeueAfter.Seconds(), 1.0, "Again")
	require.NotNil(t, res.Priority)
	assert.Equal(t, 0, *res.Priority, "Again is watch priority")
	e.Pass(t, r, nn)
	assert.Equal(t, gathers+1, nam.Gathers.Load(), "the pass after the probe landed gathers")
	assert.Equal(t, "/data/movies/b.mkv", e.Get(t, nn).Status.Naming.ExpectedPath)
	assert.Equal(t, metav1.ConditionTrue, k8s.FindCondition(e.Get(t, nn).Status.Conditions, catalogv1alpha1.MediaFileConditionProbed).Status)
}
