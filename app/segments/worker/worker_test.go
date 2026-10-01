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

package worker_test

import (
	"context"
	"errors"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/segments/worker"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/segments/decode"
)

const rate = decode.SampleRate

// fakeDecoder serves synthetic audio: noise unique to each file, with a
// shared 40 s "intro" at intro[path] seconds; and frames: credits-black for
// the last black[path] seconds, a busy picture before.
type fakeDecoder struct {
	mu        sync.Mutex
	introAt   map[string]float64
	black     map[string]int
	fail      map[string]bool
	audioHits int
}

// melody is n samples of half-second tones at random pitches: audio with
// the pitch structure Chromaprint fingerprints, unlike white noise, whose
// flat chroma makes every clip look alike.
func melody(r *rand.Rand, n int) []int16 {
	out := make([]int16, n)
	freq := 0.0
	for i := range out {
		if i%(rate/2) == 0 {
			freq = 110 * math.Pow(2, float64(r.IntN(48))/12)
		}
		out[i] = int16(8000 * math.Sin(2*math.Pi*freq*float64(i)/rate))
	}
	return out
}

var theme = melody(rand.New(rand.NewPCG(42, 42)), 40*rate)

func (f *fakeDecoder) Audio(_ context.Context, path string, _ int, fromS, durS float64) ([]int16, error) {
	f.mu.Lock()
	f.audioHits++
	f.mu.Unlock()
	if f.fail[path] {
		return nil, errors.New("decode: ffmpeg: Invalid data found when processing input")
	}
	out := melody(rand.New(rand.NewPCG(seed(path), uint64(fromS))), int(durS*rate))
	if at, ok := f.introAt[path]; ok && fromS == 0 {
		copy(out[int(at*rate):], theme)
	}
	return out, nil
}

func seed(path string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(path))
	return h.Sum64()
}

func (f *fakeDecoder) Frames(_ context.Context, path string, fromS float64) ([][]byte, error) {
	if f.fail[path] {
		return nil, errors.New("decode: ffmpeg: Invalid data found when processing input")
	}
	n := int(duration - fromS)
	out := make([][]byte, n)
	r := rand.New(rand.NewPCG(7, 7))
	for i := range out {
		fr := make([]byte, decode.GrayW*decode.GrayH)
		if i < n-f.black[path] {
			for j := range fr {
				fr[j] = byte(r.IntN(256))
			}
		}
		out[i] = fr
	}
	return out, nil
}

func (f *fakeDecoder) Frame(context.Context, string, float64) ([]byte, error) {
	return make([]byte, decode.RGBW*decode.RGBH*3), nil
}

// duration is every fake file's length, in seconds: a 400 s file has a
// 100 s start window and a 60 s end window.
const duration = 400.0

type countingDetector struct{ calls int }

func (d *countingDetector) Density([]byte, int, int) (int32, error) {
	d.calls++
	return 0, nil
}

type published struct {
	results []schema.SegmentsResult
}

func (p *published) Publish(_ context.Context, _ string, e *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	var r schema.SegmentsResult
	if err := schema.Decode(e.Schema, e.Data, &r); err != nil {
		return events.Receipt{}, err
	}
	p.results = append(p.results, r)
	return events.Receipt{}, nil
}

func (p *published) byFile() map[string]schema.SegmentsResult {
	out := map[string]schema.SegmentsResult{}
	for _, r := range p.results {
		out[r.MediaFile] = r
	}
	return out
}

type message struct{ env *events.Envelope }

func (m message) Envelope() *events.Envelope               { return m.env }
func (m message) Subject() string                          { return "" }
func (m message) Attempt() uint64                          { return 1 }
func (m message) Ack(context.Context) error                { return nil }
func (m message) Nak(context.Context, time.Duration) error { return nil }
func (m message) Term(context.Context, string) error       { return nil }
func (m message) InProgress(context.Context) error         { return nil }

func taskOf(t *testing.T, kind string, files ...string) events.Message {
	t.Helper()
	task := schema.AnalyzeTask{Namespace: "media", Key: "show-s01", Kind: kind}
	for _, f := range files {
		task.Files = append(task.Files, schema.AnalyzeFile{
			MediaFile: f, UID: "uid-" + f, Path: f, ProbeHash: "hash-" + f, DurationMs: duration * 1000, Due: true,
		})
	}
	s, data, err := schema.Encode(task)
	require.NoError(t, err)
	return message{&events.Envelope{Key: "media/show-s01", Schema: s, Data: data}}
}

func handler(t *testing.T, dec *fakeDecoder, det *countingDetector) (*worker.Handler, *published) {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	pub := &published{}
	h := &worker.Handler{Decoder: dec, Fingerprints: bus.ObjectStore(events.ObjectStoreFingerprints), Bus: pub}
	if det != nil {
		h.Detector = det
	}
	return h, pub
}

func kinds(r schema.SegmentsResult) map[string]schema.SegmentJSON {
	out := map[string]schema.SegmentJSON{}
	for _, s := range r.Segments {
		out[s.Kind] = s
	}
	return out
}

func TestASeasonGetsIntrosAndCredits(t *testing.T) {
	dec := &fakeDecoder{
		introAt: map[string]float64{"e1": 10, "e2": 30, "e3": 10, "e4": 50},
		black:   map[string]int{"e1": 30, "e2": 30, "e3": 30, "e4": 30},
	}
	h, pub := handler(t, dec, nil)
	require.NoError(t, h.Handle(context.Background(), taskOf(t, "episode", "e1", "e2", "e3", "e4")))
	got := pub.byFile()
	require.Len(t, got, 4)
	for f, at := range dec.introAt {
		r := got[f]
		assert.Equal(t, "Found", r.Result, f)
		in := kinds(r)["intro"]
		// Within the classifiers' 16-point (~2 s) window: a neighbouring tone
		// of the same pitch class blurs a region's edge by up to that much.
		assert.InDelta(t, at*1000, in.StartMs, 2000, "%s intro start", f)
		assert.InDelta(t, (at+40)*1000, in.EndMs, 2000, "%s intro end", f)
		cr := kinds(r)["credits"]
		assert.InDelta(t, (duration-30)*1000, cr.StartMs, 2000, "%s credits", f)
		assert.Equal(t, "analysis", cr.Source)
	}
}

func TestASingleFileSeasonGetsCreditsOnly(t *testing.T) {
	dec := &fakeDecoder{introAt: map[string]float64{"e1": 10}, black: map[string]int{"e1": 30}}
	h, pub := handler(t, dec, nil)
	require.NoError(t, h.Handle(context.Background(), taskOf(t, "episode", "e1")))
	k := kinds(pub.byFile()["e1"])
	assert.NotContains(t, k, "intro")
	assert.Contains(t, k, "credits")
}

func TestADecodeFailureIsThatFilesError(t *testing.T) {
	dec := &fakeDecoder{
		introAt: map[string]float64{"e1": 10, "e2": 10, "e3": 10},
		black:   map[string]int{"e1": 30, "e3": 30},
		fail:    map[string]bool{"e2": true},
	}
	h, pub := handler(t, dec, nil)
	require.NoError(t, h.Handle(context.Background(), taskOf(t, "episode", "e1", "e2", "e3")))
	got := pub.byFile()
	assert.Equal(t, "Error", got["e2"].Result)
	assert.Contains(t, got["e2"].Message, "Invalid data")
	assert.Equal(t, "Found", got["e1"].Result, "its siblings go on")
	assert.Contains(t, kinds(got["e1"]), "intro", "two decodable files still share an intro")
}

func TestCachedFingerprintsAreNotDecodedAgain(t *testing.T) {
	dec := &fakeDecoder{introAt: map[string]float64{"e1": 10, "e2": 10}, black: map[string]int{"e1": 30, "e2": 30}}
	h, _ := handler(t, dec, nil)
	require.NoError(t, h.Handle(context.Background(), taskOf(t, "episode", "e1", "e2")))
	first := dec.audioHits
	require.NoError(t, h.Handle(context.Background(), taskOf(t, "episode", "e1", "e2")))
	assert.Equal(t, first, dec.audioHits, "the second pass reads every fingerprint from the store")
}

func TestTheDNNRunsOnlyWhenSignalsAreWeak(t *testing.T) {
	det := &countingDetector{}
	h, _ := handler(t, &fakeDecoder{black: map[string]int{"m": 60}}, det)
	require.NoError(t, h.Handle(context.Background(), taskOf(t, "movie", "m")))
	assert.Zero(t, det.calls, "black credits stand on their own")

	h, pub := handler(t, &fakeDecoder{}, det)
	require.NoError(t, h.Handle(context.Background(), taskOf(t, "movie", "m")))
	assert.Positive(t, det.calls, "nothing else found: the DNN is asked")
	assert.Equal(t, "NotFound", pub.byFile()["m"].Result)
}

func TestWithoutADetectorTheStageIsSkipped(t *testing.T) {
	h, pub := handler(t, &fakeDecoder{}, nil)
	require.NoError(t, h.Handle(context.Background(), taskOf(t, "movie", "m")))
	assert.Equal(t, "NotFound", pub.byFile()["m"].Result)
}

func TestAFileNotDueIsFingerprintedButNotReported(t *testing.T) {
	dec := &fakeDecoder{introAt: map[string]float64{"e1": 10, "e2": 10}, black: map[string]int{"e1": 30, "e2": 30}}
	h, pub := handler(t, dec, nil)
	m := taskOf(t, "episode", "e1", "e2")
	var task schema.AnalyzeTask
	require.NoError(t, schema.Decode(m.Envelope().Schema, m.Envelope().Data, &task))
	task.Files[1].Due = false
	s, data, err := schema.Encode(task)
	require.NoError(t, err)
	require.NoError(t, h.Handle(context.Background(), message{&events.Envelope{Key: "media/show-s01", Schema: s, Data: data}}))
	got := pub.byFile()
	assert.Len(t, got, 1)
	assert.Contains(t, kinds(got["e1"]), "intro", "the file not due still served as e1's comparison")
}
