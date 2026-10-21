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

package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tsUpstream serves head, then gop over and over, flushing each, after
// waiting delay; it is a provider's live stream as the relay sees one.
func tsUpstream(t *testing.T, delay time.Duration, head, gop []byte) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		time.Sleep(delay)
		if _, err := w.Write(head); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		for r.Context().Err() == nil {
			time.Sleep(5 * time.Millisecond) // about 3 MB/s
			if _, err := w.Write(gop); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/1005.ts"
}

func viewer(t *testing.T, r *Relay, up string) io.Reader {
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { r.Serve(w, req, "1005", up) }))
	t.Cleanup(front.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp.Body
}

func testRelay(t *testing.T, o Options) *Relay {
	o.Tuners, o.Buffer, o.Linger, o.Backoff, o.ReadyTimeout = 1, 1<<20, time.Second, 10*time.Millisecond, 5*time.Second
	r := New(o)
	t.Cleanup(r.Close)
	return r
}

func gop() []byte {
	return cat(patPacket(), pmtPacket(0x1b), h264Key(), fill(testVideoPID, 40), fill(testAudioPID, 2), h264Frame(), fill(testVideoPID, 40))
}

// TestTheFirstViewerGetsTheUpstreamFromItsFirstByte: a provider starts a
// connection with its tables and a keyframe (tvload.win's CDN, 2026-10-21),
// which a viewer joined at the newest packet missed, so PMS's decoder began
// mid-GOP ("non-existing PPS 0 referenced") and waited for the next keyframe.
func TestTheFirstViewerGetsTheUpstreamFromItsFirstByte(t *testing.T) {
	head := cat(fill(0x11, 1), patPacket(), pmtPacket(0x1b), h264Key(), fill(testVideoPID, 3))
	r := testRelay(t, Options{})
	body := viewer(t, r, tsUpstream(t, 0, head, gop()))

	got := make([]byte, len(head))
	_, err := io.ReadFull(body, got)

	require.NoError(t, err)
	assert.Equal(t, head, got)
}

func TestALaterViewerStartsAtTheLatestKeyframeWithTheTablesFirst(t *testing.T) {
	r := testRelay(t, Options{}) // 1 MiB: past 512 KiB a viewer no longer starts at the connection
	up := tsUpstream(t, 0, nil, cat(patPacket(), pmtPacket(0x1b), fill(testAudioPID, 2), h264Key(), fill(testVideoPID, 80)))
	_, err := io.CopyN(io.Discard, viewer(t, r, up), 600_003)
	require.NoError(t, err)

	got := make([]byte, 3*packetSize)
	_, err = io.ReadFull(viewer(t, r, up), got)

	require.NoError(t, err)
	assert.Equal(t, cat(patPacket(), pmtPacket(0x1b), h264Key()), got)
}

func TestTheUpstreamsFirstByteIsTimed(t *testing.T) {
	var mu sync.Mutex
	var took []time.Duration
	r := testRelay(t, Options{OnFirstByte: func(d time.Duration) {
		mu.Lock()
		took = append(took, d)
		mu.Unlock()
	}})
	_, err := io.CopyN(io.Discard, viewer(t, r, tsUpstream(t, 80*time.Millisecond, nil, gop())), 10_000)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, took, 1)
	assert.GreaterOrEqual(t, took[0], 80*time.Millisecond)
}
