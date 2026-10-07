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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// libx265 sizes its thread pool from the node's cores, not the pod's CPU
// limit; the argv engine passes pools=<threads> (X265Params), and the
// in-process engine must too. It rides outside the plan, whose hash the
// controller and the worker compare.
func TestTheInProcessX265PoolIsThePodsThreads(t *testing.T) {
	plan := standard.Result{Video: standard.VideoPlan{
		Encoder: "libx265",
		Options: map[string]string{"crf": "24", "x265-params": "log-level=error:keyint=240"},
	}}
	hash := plan.Hash()

	got := withX265Pools(plan, 4)
	assert.Equal(t, "log-level=error:keyint=240:pools=4", got.Video.Options["x265-params"])
	assert.Equal(t, "log-level=error:keyint=240", plan.Video.Options["x265-params"], "the plan itself is not changed")
	assert.Equal(t, hash, plan.Hash())

	assert.Equal(t, plan, withX265Pools(plan, 0), "no thread count, no pool size")
	nv := standard.Result{Video: standard.VideoPlan{Encoder: "hevc_nvenc", Options: map[string]string{"qp": "23"}}}
	assert.Equal(t, nv, withX265Pools(nv, 4), "only libx265 has a thread pool")
}
