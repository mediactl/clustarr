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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// beatMessage counts heartbeats (InProgress) and can fail them.
type beatMessage struct {
	beats int
	fail  error
}

func (m *beatMessage) Envelope() *events.Envelope               { return &events.Envelope{} }
func (m *beatMessage) Subject() string                          { return "" }
func (m *beatMessage) Attempt() uint64                          { return 1 }
func (m *beatMessage) Ack(context.Context) error                { return nil }
func (m *beatMessage) Nak(context.Context, time.Duration) error { return nil }
func (m *beatMessage) Term(context.Context, string) error       { return nil }
func (m *beatMessage) InProgress(context.Context) error         { m.beats++; return m.fail }

// audioProber is a mediainfo.Prober whose only real half is ProbeAudio.
type audioProber func(ctx context.Context, path string) (mediainfo.AudioProbe, error)

func (audioProber) Probe(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	return nil, nil, errors.New("not a video")
}

func (f audioProber) ProbeAudio(ctx context.Context, path string) (mediainfo.AudioProbe, error) {
	return f(ctx, path)
}

// TestAnAudioProbeIsBounded (OD47): an audio probe that hangs (a stalled
// mount) is cut at AudioProbeTimeout, and the file freezes by its extension,
// as a failed probe always did.
func TestAnAudioProbeIsBounded(t *testing.T) {
	var (
		deadline time.Time
		had      bool
	)
	hang := func(ctx context.Context, _ string) (mediainfo.AudioProbe, error) {
		deadline, had = ctx.Deadline()
		return mediainfo.AudioProbe{}, context.DeadlineExceeded
	}
	start := time.Now()
	q, ok := FrozenFileQuality(context.Background(), hang, commonv1.MediaKindAlbum, "/data/music/a.flac")
	require.True(t, had, "the audio probe ran without a deadline")
	assert.WithinDuration(t, start.Add(AudioProbeTimeout), deadline, time.Second)
	assert.Equal(t, 15*time.Second, AudioProbeTimeout)
	wantQ, wantOK := FrozenQuality(commonv1.MediaKindAlbum, "/data/music/a.flac")
	assert.Equal(t, wantQ, q, "a timed-out probe freezes the file by its extension")
	assert.Equal(t, wantOK, ok)

	// The bound is the probe's own: a caller with a longer deadline still
	// gets AudioProbeTimeout, and one with a shorter deadline keeps it.
	long, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	start = time.Now()
	FrozenFileQuality(long, hang, commonv1.MediaKindAlbum, "/data/music/a.flac")
	assert.WithinDuration(t, start.Add(AudioProbeTimeout), deadline, time.Second)
}

// TestAnAudioProbeHeartbeatsAndIsBounded (W4.86): a music file's probe
// heartbeats immediately before it runs, so its 15 s bound lies inside the
// delivery's ack deadline, and it runs under that bound; a failed heartbeat
// runs no probe and is the error the inspect returns.
func TestAnAudioProbeHeartbeatsAndIsBounded(t *testing.T) {
	m := &beatMessage{}
	var (
		beatsAtProbe int
		deadline     time.Time
		bounded      bool
	)
	w := NewWorker(nil, nil, audioProber(func(ctx context.Context, _ string) (mediainfo.AudioProbe, error) {
		beatsAtProbe = m.beats
		deadline, bounded = ctx.Deadline()
		return mediainfo.AudioProbe{Codec: "flac", SampleBits: 24}, nil
	}))
	var hbErr error
	start := time.Now()
	q, known := FrozenFileQuality(context.Background(), w.audioProbe(m, &hbErr), commonv1.MediaKindAlbum, "/data/music/a.flac")
	require.NoError(t, hbErr)
	assert.Equal(t, 1, beatsAtProbe, "one heartbeat, immediately before the probe")
	require.True(t, bounded, "the probe ran without a deadline")
	assert.WithinDuration(t, start.Add(AudioProbeTimeout), deadline, time.Second)
	require.True(t, known)
	assert.Equal(t, "24bit Lossless", q.Name, "the probe, not the extension, settled a 24-bit FLAC")

	m2 := &beatMessage{fail: errors.New("ack deadline lost")}
	probed := false
	w2 := NewWorker(nil, nil, audioProber(func(context.Context, string) (mediainfo.AudioProbe, error) {
		probed = true
		return mediainfo.AudioProbe{}, nil
	}))
	hbErr = nil
	FrozenFileQuality(context.Background(), w2.audioProbe(m2, &hbErr), commonv1.MediaKindAlbum, "/data/music/a.flac")
	require.ErrorContains(t, hbErr, "ack deadline lost")
	assert.False(t, probed, "a failed heartbeat runs no probe")

	hbErr = nil
	assert.Nil(t, NewWorker(nil, nil, nil).audioProbe(m, &hbErr), "no prober: the file freezes by extension, as before")
}
