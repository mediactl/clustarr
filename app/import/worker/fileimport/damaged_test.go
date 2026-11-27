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

package fileimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/internal/testmedia"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/mediainfo/native"
)

// A file whose bytes have holes probes as whole media: its header is
// intact. Fena: Pirate Princess S01E01 and E11 were imported so, 17% and
// 40% of their articles never fetched, and transcoded with a quarter and
// more of their frames gone (talos, 2026-11-17). Read end to end by the
// domain's own prober, such a file is refused as the release's fault, so
// the release is blocklisted and searched for again; the same file whole
// imports.
func TestADamagedFileIsTheReleasesFault(t *testing.T) {
	p, err := native.New()
	if err != nil {
		t.Skipf("no FFmpeg 9 with its shim: %v", err)
	}
	whole := testmedia.Make(t, "Show.S01E01.1080p.WEB-DL.mkv", testmedia.Spec{
		Duration: 60 * time.Second,
		Video: []testmedia.Video{{
			Source: "testsrc2=size=320x180:rate=24", Options: map[string]string{"preset": "ultrafast", "g": "24"},
		}},
		Audio: []testmedia.Audio{{Source: "sine=frequency=440"}},
	})
	w := &Worker{Prober: p}
	ctx := context.Background()

	mi, _, refused, err := w.probeVideo(ctx, nopMessage{}, whole, filepath.Base(whole))
	require.NoError(t, err)
	require.NoError(t, refused)
	require.NotNil(t, mi)

	// What a usenet transfer leaves where articles never arrived: zeros in
	// a file of the full size.
	b, err := os.ReadFile(whole)
	require.NoError(t, err)
	clear(b[len(b)*2/10 : len(b)*3/10])
	clear(b[len(b)*6/10 : len(b)*7/10])
	holed := filepath.Join(t.TempDir(), filepath.Base(whole))
	require.NoError(t, os.WriteFile(holed, b, 0o644))

	mi, _, refused, err = w.probeVideo(ctx, nopMessage{}, holed, filepath.Base(holed))
	require.NoError(t, err)
	assert.Nil(t, mi)
	require.ErrorIs(t, refused, mediainfo.ErrDamaged)
	r := notMediaRejection(filepath.Base(holed), refused)
	assert.Equal(t, string(commonv1.ImportClassReleaseFault), r.Class)
	assert.Contains(t, r.Text, "not a whole media file")
	assert.Contains(t, r.Text, "is missing from every stream")
}

// okProber probes every file as media, and reads it end to end as health
// says.
type okProber struct {
	health func(ctx context.Context) (mediainfo.Health, error)
}

func (okProber) Probe(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	return &commonv1.MediaInfo{}, &mediainfo.Raw{}, nil
}

func (okProber) ProbeAudio(context.Context, string) (mediainfo.AudioProbe, error) {
	return mediainfo.AudioProbe{}, nil
}

func (p okProber) Health(ctx context.Context, _ string) (mediainfo.Health, error) {
	return p.health(ctx)
}

// lapsedMessage is a delivery whose heartbeats fail.
type lapsedMessage struct{ nopMessage }

func (lapsedMessage) InProgress(context.Context) error { return errors.New("the delivery lapsed") }

// A read that could not finish for a reason of its own judges nothing, and
// the file imports as it did before files were read end to end; a heartbeat
// that fails is the import's error, as the file loop's own is.
func TestAHealthReadThatFailsJudgesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.mkv")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	w := &Worker{Prober: okProber{health: func(context.Context) (mediainfo.Health, error) {
		return mediainfo.Health{}, errors.New("native: no decoder for the stream")
	}}}
	mi, _, refused, err := w.probeVideo(context.Background(), nopMessage{}, path, "x.mkv")
	require.NoError(t, err)
	require.NoError(t, refused)
	require.NotNil(t, mi)

	_, _, _, err = w.probeVideo(context.Background(), lapsedMessage{}, path, "x.mkv")
	require.ErrorContains(t, err, "the delivery lapsed")
}

// headProber is a prober whose probe answers probeErr and whose end-to-end
// read answers health: a file's header and its bytes, judged apart.
type headProber struct {
	probeErr error
	health   func(ctx context.Context) (mediainfo.Health, error)
	reads    *int
}

func (p headProber) Probe(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	if p.probeErr != nil {
		return nil, nil, p.probeErr
	}
	return &commonv1.MediaInfo{}, &mediainfo.Raw{}, nil
}

func (headProber) ProbeAudio(context.Context, string) (mediainfo.AudioProbe, error) {
	return mediainfo.AudioProbe{}, nil
}

func (p headProber) Health(ctx context.Context, _ string) (mediainfo.Health, error) {
	if p.reads != nil {
		*p.reads++
	}
	return p.health(ctx)
}

// A file the import ran out of time to judge is neither imported nor the
// release's fault: it is rejected as transient, so the import is inspected
// again. On talos, 2026-11-27, one Gosick release's eleven episodes arrived
// with fourteen to sixteen of their twenty-four minutes zeroed; ten were
// refused, and S01E22's probe hit its fifteen seconds while the two import
// pods read the others end to end -- "could not probe the file; importing it
// under its name-derived quality" -- so it went into the library with no
// end-to-end read at all (owner, 2026-11-27: "Do all 3"). The errors are the
// ones the domain's prober returns: its probe abandoned at the deadline, its
// read cut off by its context.
func TestAFileTheImportRanOutOfTimeToJudge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Gosick.S01E22.1080p.CR.WEB-DL.AAC2.0.H.264-BiOMA.mkv")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	rel := filepath.Base(path)
	whole := func(context.Context) (mediainfo.Health, error) { return mediainfo.Health{Judged: true}, nil }

	for _, tc := range []struct {
		name   string
		prober headProber
		want   string
	}{
		{
			"the probe abandoned at its deadline, as the prober reports it",
			headProber{probeErr: fmt.Errorf("mediainfo: probe %s: %w: %w", path, mediainfo.ErrProbeAbandoned, context.DeadlineExceeded), health: whole},
			"its probe did not answer in 15s",
		},
		{
			"the probe cut off by its context",
			headProber{probeErr: fmt.Errorf("mediainfo: probe %s: %w", path, context.DeadlineExceeded), health: whole},
			"its probe did not answer in 15s",
		},
		{
			"the end-to-end read out of its bound",
			headProber{health: func(context.Context) (mediainfo.Health, error) { return mediainfo.Health{}, context.DeadlineExceeded }},
			"reading it end to end did not finish in 1m0s",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			tc.prober.reads = &reads
			w := &Worker{Prober: tc.prober}
			mi, _, refused, err := w.probeVideo(context.Background(), nopMessage{}, path, rel)
			require.NoError(t, err, "the import goes on to its other files")
			assert.Nil(t, mi)
			require.ErrorIs(t, refused, errUnjudged)
			if tc.prober.probeErr != nil {
				assert.Zero(t, reads, "a probe out of time is not followed by a read of the whole file")
			}

			r := notMediaRejection(rel, refused)
			assert.Equal(t, string(commonv1.ImportClassTransient), r.Class, "retried, never blocklisted")
			assert.Contains(t, r.Text, rel)
			assert.Contains(t, r.Text, tc.want)
			assert.Contains(t, r.Text, "so it was not imported")
		})
	}

	// The handler's own context ending is the walk cut short, not a file
	// out of time: the delivery is redelivered.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &Worker{Prober: headProber{probeErr: context.Canceled, health: whole}}
	_, _, refused, err := w.probeVideo(ctx, nopMessage{}, path, rel)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, refused)
}

// A header the prober could not describe says nothing of the bytes behind
// it, so the file is still read end to end before it imports under its
// name: holed, it is the release's fault as any damaged file is; whole, it
// imports by name as a file whose probe failed always did; one the read
// cannot open as media is refused as no media. Until 2026-11-27 a failed
// probe skipped the read.
func TestAFileWhoseProbeFailedIsStillReadEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.mkv")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	probeErr := errors.New("mediainfo: probe /x.mkv: no video stream could be described")

	holed := mediainfo.Health{Judged: true, Duration: 24 * time.Minute, Holes: 10, Missing: 15 * time.Minute, Largest: 4 * time.Minute}
	require.True(t, holed.Damaged())
	reads := 0
	w := &Worker{Prober: headProber{probeErr: probeErr, reads: &reads, health: func(context.Context) (mediainfo.Health, error) { return holed, nil }}}
	mi, _, refused, err := w.probeVideo(context.Background(), nopMessage{}, path, "x.mkv")
	require.NoError(t, err)
	assert.Nil(t, mi)
	assert.Equal(t, 1, reads)
	require.ErrorIs(t, refused, mediainfo.ErrDamaged)
	assert.Equal(t, string(commonv1.ImportClassReleaseFault), notMediaRejection("x.mkv", refused).Class)

	w.Prober = headProber{probeErr: probeErr, health: func(context.Context) (mediainfo.Health, error) { return mediainfo.Health{Judged: true}, nil }}
	mi, raw, refused, err := w.probeVideo(context.Background(), nopMessage{}, path, "x.mkv")
	require.NoError(t, err)
	require.NoError(t, refused, "read whole: it imports under its name-derived quality, as before")
	assert.Nil(t, mi, "no description: the probe gave none")
	assert.Nil(t, raw)

	w.Prober = headProber{probeErr: probeErr, health: func(context.Context) (mediainfo.Health, error) {
		return mediainfo.Health{}, fmt.Errorf("%w: no demuxer reads it", mediainfo.ErrNotMedia)
	}}
	_, _, refused, err = w.probeVideo(context.Background(), nopMessage{}, path, "x.mkv")
	require.NoError(t, err)
	require.ErrorIs(t, refused, mediainfo.ErrNotMedia)
	assert.Equal(t, string(commonv1.ImportClassReleaseFault), notMediaRejection("x.mkv", refused).Class)
}
