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
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// planRecorder is a scripted engine that keeps the plan it encodes.
type planRecorder struct {
	*scriptedEngine
	mu   sync.Mutex
	plan standard.Result
}

func (e *planRecorder) Encode(ctx context.Context, plan standard.Result, tier transcode.Tier, in, out string, p func(transcode.Progress)) (transcode.EncodeResult, error) {
	e.mu.Lock()
	e.plan = plan
	e.mu.Unlock()
	return e.scriptedEngine.Encode(ctx, plan, tier, in, out, p)
}

// withAudio adds a Japanese, an English and a Spanish track to a reading.
func withAudio(raw *mediainfo.Raw) (*commonv1alpha1.MediaInfo, *mediainfo.Raw) {
	for i, l := range []string{"jpn", "eng", "spa"} {
		raw.Streams = append(raw.Streams, &ffprobe.Stream{
			Index: i + 1, CodecType: string(ffprobe.StreamAudio), CodecName: "aac", Channels: 2, ChannelLayout: "stereo",
			SampleRate: "48000", TagList: ffprobe.Tags{"language": l},
		})
	}
	return mediainfo.FromRaw(raw), raw
}

// The worker plans with the task's original language, as the manager did:
// "original" in audio.languages is the item's original language, which no
// probe can read, so it rides in the task (task.OriginalLanguage). Without
// it the worker would read the original as unknown and keep every track.
func TestTheWorkerPlansWithTheItemsOriginalLanguage(t *testing.T) {
	c := newSafetyCase(t, "sdr", "sdr")
	c.tk.Profile.Spec.Audio.Languages = []string{"en", "original"}
	c.tk.OriginalLanguage = "ja"
	c.eng.mi, c.eng.raw = withAudio(c.eng.raw)
	info, err := transcode.FromProbe(c.eng.mi, c.eng.raw)
	require.NoError(t, err)
	info.OriginalLanguage = "ja"
	want := standard.Plan(info, jobspec.StandardProfile(c.tk.Profile.Name, c.tk.Profile.Hash, c.tk.Profile.Spec),
		standard.Hardware{Tier: transcode.TierCPUx265})
	c.tk.PlanHash = want.Hash()

	rec := &planRecorder{scriptedEngine: c.eng}
	out := Process(context.Background(), c.tk, Options{
		DataDir: dataDirOf(c), Engine: rec, Measurement: &transcode.Measurement{Tier: transcode.TierCPUx265},
	})
	require.NoError(t, out.Err)
	var langs []string
	for _, a := range rec.plan.Audio {
		langs = append(langs, a.Language)
	}
	assert.Equal(t, []string{"jpn", "eng"}, langs, "the Spanish track is dropped")
	assert.Equal(t, want.Hash(), rec.plan.Hash(), "the worker's plan is the manager's")
}

// dataDirOf is a safety case's data volume, four levels above its source.
func dataDirOf(c *safetyCase) string {
	d := c.local
	for range 4 {
		d = filepath.Dir(d)
	}
	return d
}
