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

package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// The worker plans with the task's crop target, as the manager did: the
// shape a series is kept in is no fact of the file, so it rides in the
// task (task.CropAspectMilli), and the worker's plan from its live probe
// cuts the same rectangle under the same hash. Without it the worker
// would encode the whole frame, and the output is final.
func TestTheWorkerPlansWithTheTasksCropTarget(t *testing.T) {
	c := newSafetyCase(t, "sdr", "sdr")
	c.tk.CropAspectMilli = 1333
	info, err := transcode.FromProbe(c.eng.mi, c.eng.raw)
	require.NoError(t, err)
	require.Equal(t, int32(1778), info.Video[0].DisplayAspectMilli, "the reading is a 16:9 picture")
	info.CropAspectMilli = 1333
	want := standard.Plan(info, jobspec.StandardProfile(c.tk.Profile.Name, c.tk.Profile.Hash, c.tk.Profile.Spec),
		standard.Hardware{Tier: transcode.TierCPUx265})
	require.Equal(t, &standard.Crop{Width: 2880, Height: 2160, X: 480}, want.Video.Crop)
	c.tk.PlanHash = want.Hash()

	rec := &planRecorder{scriptedEngine: c.eng}
	out := Process(context.Background(), c.tk, Options{
		DataDir: dataDirOf(c), Engine: rec, Measurement: &transcode.Measurement{Tier: transcode.TierCPUx265},
	})
	require.NoError(t, out.Err)
	assert.Equal(t, want.Video.Crop, rec.plan.Video.Crop, "the 16:9 frame loses its sides for a series kept 4:3")
	assert.Equal(t, want.Hash(), rec.plan.Hash(), "the worker's plan is the manager's")
}
