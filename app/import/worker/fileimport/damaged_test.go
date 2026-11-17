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

// A read that could not finish judges nothing, and the file imports as it
// did before files were read end to end; a heartbeat that fails is the
// import's error, as the file loop's own is.
func TestAHealthReadThatFailsJudgesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.mkv")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	w := &Worker{Prober: okProber{health: func(context.Context) (mediainfo.Health, error) {
		return mediainfo.Health{}, context.DeadlineExceeded
	}}}
	mi, _, refused, err := w.probeVideo(context.Background(), nopMessage{}, path, "x.mkv")
	require.NoError(t, err)
	require.NoError(t, refused)
	require.NotNil(t, mi)

	_, _, _, err = w.probeVideo(context.Background(), lapsedMessage{}, path, "x.mkv")
	require.ErrorContains(t, err, "the delivery lapsed")
}
