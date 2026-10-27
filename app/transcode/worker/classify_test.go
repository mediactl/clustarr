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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// stagedErr is an engine failure that names its stage, as engine.Error does.
type stagedErr struct{ stage string }

func (e stagedErr) Error() string       { return "engine: " + e.stage + ": invalid argument" }
func (e stagedErr) EngineStage() string { return e.stage }

// erringEngine is a scripted engine whose encode fails with err.
type erringEngine struct {
	*scriptedEngine
	err error
}

func (e erringEngine) Encode(context.Context, standard.Result, transcode.Tier, string, string, func(transcode.Progress)) (transcode.EncodeResult, error) {
	return transcode.EncodeResult{}, e.err
}

// A failed encode on a GPU tier is the GPU's only when the plan encodes its
// video there and the engine failed in the video stage (or did not say
// where). A remux encodes nothing on a GPU, and a demux, mux or audio
// failure is the source's or the muxer's, which the CPU class meets the
// same way: 49 Up's and 63 Up's damaged sources were filed GPUEncodeFailed
// (2026-10-15), which under hardware auto would send them to the CPU.
func TestOnlyAVideoEncodeFailureOnAGPUIsTheGPUs(t *testing.T) {
	encode := standard.Result{Decision: standard.DecisionEncode}
	remux := standard.Result{Decision: standard.DecisionCopyVideo}
	for _, c := range []struct {
		name string
		plan standard.Result
		tier transcode.Tier
		err  error
		want task.Reason
	}{
		{"an encode failing in its video stage", encode, transcode.TierQSV, stagedErr{"video"}, task.ReasonGPUEncodeFailed},
		{"an encode failing where the engine does not say", encode, transcode.TierNVENC, errors.New("nvenc: no device"), task.ReasonGPUEncodeFailed},
		{"an encode failing in the muxer", encode, transcode.TierQSV, stagedErr{"mux"}, ""},
		{"an encode failing in an audio stage", encode, transcode.TierQSV, stagedErr{"audio:0"}, ""},
		{"a remux failing in the muxer", remux, transcode.TierQSV, stagedErr{"mux"}, ""},
		{"a remux failing where the engine does not say", remux, transcode.TierQSV, errors.New("x"), ""},
		{"a CPU encode failing in its video stage", encode, transcode.TierCPUx265, stagedErr{"video"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &runner{o: Options{Engine: erringEngine{&scriptedEngine{}, c.err}}.withDefaults()}
			err := r.encodeFFgo(context.Background(), c.plan, c.tier, "in", "out", 0, nil)
			require.Error(t, err)
			var f *failure
			require.ErrorAs(t, err, &f)
			assert.Equal(t, c.want, f.reason)
			assert.Equal(t, ExitRetriable, f.code)
		})
	}
}
