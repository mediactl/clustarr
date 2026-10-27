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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// nopMessage is a delivery whose heartbeats succeed.
type nopMessage struct{}

func (nopMessage) Envelope() *events.Envelope               { return &events.Envelope{} }
func (nopMessage) Subject() string                          { return "" }
func (nopMessage) Attempt() uint64                          { return 1 }
func (nopMessage) Ack(context.Context) error                { return nil }
func (nopMessage) Nak(context.Context, time.Duration) error { return nil }
func (nopMessage) Term(context.Context, string) error       { return nil }
func (nopMessage) InProgress(context.Context) error         { return nil }

// errProber fails every probe with err.
type errProber struct{ err error }

func (p errProber) Probe(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	return nil, nil, p.err
}

func (p errProber) ProbeAudio(context.Context, string) (mediainfo.AudioProbe, error) {
	return mediainfo.AudioProbe{}, p.err
}

// A file the prober finds is no media -- empty, or bytes no demuxer reads --
// is refused as the release's fault, so the release is blocklisted and
// searched for again. Before, every probe failure imported the file under
// its name-derived quality: a fake release's three encrypted "remuxes", and
// empty and zero-headed downloads, reached the library on talos
// (2026-10-15). A probe that could not run still imports by name, as it did.
func TestAFileThatIsNotMediaIsTheReleasesFault(t *testing.T) {
	w := &Worker{Prober: errProber{fmt.Errorf("mediainfo: probe /x.mkv: %w", mediainfo.ErrNotMedia)}}
	mi, _, notMedia, err := w.probeVideo(context.Background(), nopMessage{}, "/x.mkv", "x.mkv")
	require.NoError(t, err)
	assert.Nil(t, mi)
	require.ErrorIs(t, notMedia, mediainfo.ErrNotMedia)
	r := notMediaRejection("x.mkv", notMedia)
	assert.Equal(t, string(commonv1.ImportClassReleaseFault), r.Class)
	assert.Contains(t, r.Text, "x.mkv")

	w.Prober = errProber{errors.New("mediainfo: probe /x.mkv: context deadline exceeded")}
	mi, _, notMedia, err = w.probeVideo(context.Background(), nopMessage{}, "/x.mkv", "x.mkv")
	require.NoError(t, err)
	assert.Nil(t, mi)
	assert.NoError(t, notMedia, "a probe that could not run imports under the name, as before")
}
