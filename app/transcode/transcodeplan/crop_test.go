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

package transcodeplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// The manager plans a file of a series that crops (Series.CropTarget, read
// by the adapter into Inputs.CropAspectMilli) as an encode that cuts it to
// the shape the series is kept in: a 4:3 episode of a widescreen series
// loses its top and bottom. The same file with no target is planned whole,
// under another plan hash.
func TestThePlanCropsToTheShapeTheSeriesIsKeptIn(t *testing.T) {
	tp := newProfile("hevc", true, t0, nil)
	st := probed("ph-1")
	st.MediaInfo.Width, st.MediaInfo.Height, st.MediaInfo.DisplayAspectMilli = 1440, 1080, 1333
	mf := heatFile(st)
	hash := jobspec.ProfileHash(tp.Spec)

	p, fail := PlanFor(mf.Spec.Path, &tp, hash, mf, st, nil, Device{}, "", 1778)
	require.Nil(t, fail)
	require.False(t, p.Skips(), p.Plan.Reason)
	assert.Equal(t, &standard.Crop{Width: 1440, Height: 810, Y: 134}, p.Plan.Video.Crop)
	assert.Equal(t, int32(810), p.Plan.Expect.Height)

	whole, fail := PlanFor(mf.Spec.Path, &tp, hash, mf, st, nil, Device{}, "", 0)
	require.Nil(t, fail)
	assert.Nil(t, whole.Plan.Video.Crop)
	assert.NotEqual(t, whole.Plan.Hash(), p.Plan.Hash())
}

// The task carries the crop target, on a republish and on a first
// dispatch (which builds its task itself), so the worker's plan from its
// live probe is the manager's: a worker that planned without it would
// encode the whole frame into a final file of the wrong shape.
func TestTheTaskCarriesTheCropTarget(t *testing.T) {
	in := queuedIn(t)
	in.CropAspectMilli = 1778
	d := pass(in)
	require.NotNil(t, d.Publish, "a requested seq inside the window is republished")
	assert.Equal(t, int32(1778), d.Publish.CropAspectMilli)

	in = dispatchIn(t)
	in.CropAspectMilli = 1778
	d = pass(in)
	require.NotNil(t, d.Publish, "the dispatch publishes its task")
	assert.Equal(t, int32(1778), d.Publish.CropAspectMilli)

	in = dispatchIn(t)
	d = pass(in)
	require.NotNil(t, d.Publish)
	assert.Zero(t, d.Publish.CropAspectMilli, "no target, no crop")
}
