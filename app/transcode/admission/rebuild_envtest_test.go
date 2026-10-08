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

package admission_test

import (
	"context"
	"fmt"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/admission"
)

// TestRebuildCountsEveryInFlightFileFromTheAPIServer is §5.6 step 1 and D6:
// 40 files across every phase, the cache reader stubbed empty, so only the
// APIReader's server-filtered Lists on status.transcode.phase count them.
func TestRebuildCountsEveryInFlightFileFromTheAPIServer(t *testing.T) {
	const ns = "rebuild-counts"
	c := requireEnvtest(t, ns)
	phases := []catalogv1alpha1.TranscodePhase{catalogv1alpha1.TranscodePhasePending, catalogv1alpha1.TranscodePhasePlanned,
		catalogv1alpha1.TranscodePhaseQueued, catalogv1alpha1.TranscodePhaseRunning, catalogv1alpha1.TranscodePhaseSwapping,
		catalogv1alpha1.TranscodePhaseSucceeded, catalogv1alpha1.TranscodePhaseFailed, catalogv1alpha1.TranscodePhaseSkipped}
	inFlight := map[string]int{}
	window := 0
	for i := range 40 {
		p, class := phases[i%len(phases)], []string{"cpu", "nvidia"}[i%2]
		seed(t, c, ns, fmt.Sprintf("rebuild-%02d", i), block(p, int64(i+1), class))
		switch p {
		case catalogv1alpha1.TranscodePhaseQueued, catalogv1alpha1.TranscodePhaseRunning:
			inFlight[class]++
			window++
		case catalogv1alpha1.TranscodePhasePending, catalogv1alpha1.TranscodePhasePlanned, catalogv1alpha1.TranscodePhaseSwapping:
			window++
		}
	}
	// A file in another namespace the ledger does not watch.
	other := "rebuild-counts-other"
	requireEnvtest(t, other)
	seed(t, c, other, "elsewhere", block(catalogv1alpha1.TranscodePhaseRunning, 1, "cpu"))

	l := admission.NewLedger(admission.Options{Slots: map[string]int32{"cpu": 8, "nvidia": 2}, Window: 64})
	assert.False(t, l.Rebuilt())
	api := &countingReader{Reader: c}
	require.NoError(t, l.Rebuild(context.Background(), admission.RebuildInputs{
		APIReader: api, Cache: emptyReader{}, Namespaces: []string{ns},
	}))
	assert.True(t, l.Rebuilt())
	assert.Equal(t, 7, api.lists, "five transcode window phases and two graft phases, each its own List")
	assert.Equal(t, window, api.items, "the apiserver filtered: no verdict or finished file came back")
	require.NotZero(t, inFlight["cpu"])
	require.NotZero(t, inFlight["nvidia"])
	assert.Equal(t, inFlight["cpu"], l.InFlight(transcodev1alpha1.HardwareCPU))
	assert.Equal(t, inFlight["nvidia"], l.InFlight(transcodev1alpha1.HardwareNVIDIA))
	assert.Equal(t, window, l.Open("hevc"))
}

// TestRebuildSeesALastMomentDispatch: a Queued write by a direct client
// immediately before Rebuild is counted (status first, §5.2 decision 2).
func TestRebuildSeesALastMomentDispatch(t *testing.T) {
	const ns = "rebuild-last-moment"
	c := requireEnvtest(t, ns)
	seed(t, c, ns, "last-moment", block(catalogv1alpha1.TranscodePhaseQueued, 9, "cpu"))
	l := admission.NewLedger(admission.Options{Slots: map[string]int32{"cpu": 1}, Window: 32})
	require.NoError(t, l.Rebuild(context.Background(), admission.RebuildInputs{APIReader: c, Cache: emptyReader{}, Namespaces: []string{ns}}))
	assert.Equal(t, 1, l.InFlight(transcodev1alpha1.HardwareCPU), "the one slot is taken")
	assert.Equal(t, 1, l.Open("hevc"))
}

// countingReader counts the Lists through it and the items they return.
type countingReader struct {
	client.Reader
	lists, items int
}

func (r *countingReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.Reader.List(ctx, list, opts...); err != nil {
		return err
	}
	r.lists++
	if l, ok := list.(*catalogv1alpha1.MediaFileList); ok {
		r.items += len(l.Items)
	}
	return nil
}
