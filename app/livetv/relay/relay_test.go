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

package relay_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/livetv/relay"
)

type upstream struct {
	srv   *httptest.Server
	conns sync.Map // path -> *atomic.Int32
	gen   atomic.Int32
	hls   atomic.Bool
	// userAgent is the last request's User-Agent.
	userAgent atomic.Value
}

// newUpstream serves an endless MPEG-TS-like stream (188-byte packets, sync
// byte 0x47) on every path, counting connections per path. kill ends every
// live connection; the next one streams again.
func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := u.conns.LoadOrStore(r.URL.Path, new(atomic.Int32))
		c.(*atomic.Int32).Add(1)
		u.userAgent.Store(r.Header.Get("User-Agent"))
		if u.hls.Load() {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n")
			return
		}
		gen := u.gen.Load()
		w.Header().Set("Content-Type", "video/mp2t")
		pkt := make([]byte, 188)
		for i := 0; ; i++ {
			pkt[0] = 0x47
			for j := 1; j < 188; j++ {
				pkt[j] = byte(i)
			}
			if r.Context().Err() != nil || u.gen.Load() != gen {
				return
			}
			if _, err := w.Write(pkt); err != nil {
				return
			}
			if i%64 == 0 {
				w.(http.Flusher).Flush()
				time.Sleep(time.Millisecond)
			}
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) kill() { u.gen.Add(1) }

func (u *upstream) connections(path string) int32 {
	c, ok := u.conns.Load(path)
	if !ok {
		return 0
	}
	return c.(*atomic.Int32).Load()
}

func newRelay(t *testing.T, tuners int, linger time.Duration) *relay.Relay {
	r := relay.New(relay.Options{
		HTTP: http.DefaultClient, Tuners: tuners, Buffer: 1 << 20, Linger: linger,
		Retries: 3, Backoff: 10 * time.Millisecond, ReadyTimeout: 5 * time.Second, UserAgent: "clustarr-test",
	})
	t.Cleanup(r.Close)
	return r
}

// watch serves one viewer through the relay and returns the response and a cancel.
func watch(t *testing.T, r *relay.Relay, channel, up string) (*http.Response, context.CancelFunc) {
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { r.Serve(w, req, channel, up) }))
	t.Cleanup(front.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	return resp, cancel
}

func TestTwoViewersShareOneUpstreamAndOneTuner(t *testing.T) {
	u := newUpstream(t)
	r := newRelay(t, 1, time.Second)
	a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	b, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	for _, resp := range []*http.Response{a, b} {
		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "video/mp2t", resp.Header.Get("Content-Type"))
		_, err := io.CopyN(io.Discard, resp.Body, 1<<20)
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), u.connections("/1001.ts"))
	assert.Equal(t, 1, r.InUse())
	assert.Equal(t, 2, r.Viewers())
}

// ended reports when a viewer's response body ends.
func ended(resp *http.Response) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(done)
	}()
	return done
}

func TestANewChannelTakesTheTunerOfAWatchedOne(t *testing.T) {
	// The owner's one-connection account, 2026-10-21: PMS keeps reading the
	// channel its client left for 30 s to 3 min, so refusing the new one
	// failed every channel change with "Could not tune".
	u := newUpstream(t)
	r := newRelay(t, 1, 10*time.Second)
	a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	require.Equal(t, http.StatusOK, a.StatusCode)
	_, _ = io.CopyN(io.Discard, a.Body, 1<<16)
	aEnded := ended(a)

	b, _ := watch(t, r, "1002", u.srv.URL+"/1002.ts")
	require.Equal(t, http.StatusOK, b.StatusCode)
	_, err := io.CopyN(io.Discard, b.Body, 1<<16)
	require.NoError(t, err)
	select {
	case <-aEnded:
	case <-time.After(2 * time.Second):
		t.Fatal("the taken channel's viewer is still streaming")
	}
	assert.Equal(t, 1, r.InUse())
	assert.Equal(t, 1, r.Viewers())
}

func TestATakenChannelDoesNotTakeTheTunerBackAtOnce(t *testing.T) {
	u := newUpstream(t)
	r := relay.New(relay.Options{
		HTTP: http.DefaultClient, Tuners: 1, Buffer: 1 << 20, Linger: 10 * time.Second,
		Retries: 3, Backoff: 10 * time.Millisecond, ReadyTimeout: 5 * time.Second, Reclaim: 300 * time.Millisecond,
	})
	t.Cleanup(r.Close)
	a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, _ = io.CopyN(io.Discard, a.Body, 1<<16)
	b, _ := watch(t, r, "1002", u.srv.URL+"/1002.ts")
	require.Equal(t, http.StatusOK, b.StatusCode)
	_, _ = io.CopyN(io.Discard, b.Body, 1<<16)

	// PMS's leftover reader reopening the channel it lost.
	again, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	assert.Equal(t, http.StatusServiceUnavailable, again.StatusCode, "no tug of war")

	time.Sleep(400 * time.Millisecond)
	back, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	assert.Equal(t, http.StatusOK, back.StatusCode, "past Reclaim, a viewer may change back")
}

func TestTheStreamWatchedLongestAgoIsTaken(t *testing.T) {
	u := newUpstream(t)
	r := newRelay(t, 2, 10*time.Second)
	a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, _ = io.CopyN(io.Discard, a.Body, 1<<16)
	aEnded := ended(a)
	time.Sleep(20 * time.Millisecond)
	b, _ := watch(t, r, "1002", u.srv.URL+"/1002.ts")
	_, _ = io.CopyN(io.Discard, b.Body, 1<<16)

	c, _ := watch(t, r, "1003", u.srv.URL+"/1003.ts")
	require.Equal(t, http.StatusOK, c.StatusCode)
	select {
	case <-aEnded:
	case <-time.After(2 * time.Second):
		t.Fatal("1001, watched longest ago, was not taken")
	}
	_, err := io.CopyN(io.Discard, b.Body, 1<<16)
	assert.NoError(t, err, "1002 keeps streaming")
	assert.Equal(t, 2, r.InUse())
}

func TestANewViewerStartsOnAPacketBoundary(t *testing.T) {
	u := newUpstream(t)
	r := newRelay(t, 1, time.Second)
	a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, _ = io.CopyN(io.Discard, a.Body, 100_003)
	b, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	head := make([]byte, 188*4)
	_, err := io.ReadFull(b.Body, head)
	require.NoError(t, err)
	for i := 0; i < 4; i++ {
		assert.Equal(t, byte(0x47), head[i*188], "sync byte at every 188")
	}
}

func TestASlowViewerIsCutOffAndTheOtherIsNot(t *testing.T) {
	u := newUpstream(t)
	r := newRelay(t, 1, time.Second)
	slow, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	fast, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, err := io.CopyN(io.Discard, fast.Body, 16<<20) // 16 buffers' worth while slow reads nothing
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, slow.Body)
	assert.NoError(t, err, "the slow viewer's response ends")
	_, err = io.CopyN(io.Discard, fast.Body, 1<<20)
	assert.NoError(t, err, "the fast viewer keeps streaming")
}

func TestAnUpstreamResetIsReopened(t *testing.T) {
	u := newUpstream(t)
	r := newRelay(t, 1, time.Second)
	a, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, _ = io.CopyN(io.Discard, a.Body, 1<<20)
	u.kill()
	_, err := io.CopyN(io.Discard, a.Body, 2<<20)
	require.NoError(t, err, "the viewer stays attached across the reopen")
	assert.Equal(t, int32(2), u.connections("/1001.ts"))
}

func TestAVanishedViewerFreesTheTunerAfterLinger(t *testing.T) {
	u := newUpstream(t)
	r := newRelay(t, 1, 50*time.Millisecond)
	a, cancel := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, _ = io.CopyN(io.Discard, a.Body, 1<<16)
	cancel() // no clean close: the client's context is gone
	require.Eventually(t, func() bool { return r.InUse() == 0 && r.Viewers() == 0 }, 2*time.Second, 10*time.Millisecond)
	b, _ := watch(t, r, "1002", u.srv.URL+"/1002.ts")
	assert.Equal(t, http.StatusOK, b.StatusCode, "the freed tuner takes another channel")
}

func TestARejoinInsideLingerReusesTheUpstream(t *testing.T) {
	u := newUpstream(t)
	r := newRelay(t, 1, time.Second)
	a, cancel := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, _ = io.CopyN(io.Discard, a.Body, 1<<16)
	cancel()
	b, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, err := io.CopyN(io.Discard, b.Body, 1<<16)
	require.NoError(t, err)
	assert.Equal(t, int32(1), u.connections("/1001.ts"))
}

func TestAnHLSAnswerIsRefused(t *testing.T) {
	u := newUpstream(t)
	u.hls.Store(true)
	var mu sync.Mutex
	var reasons []string
	r := relay.New(relay.Options{
		HTTP: http.DefaultClient, Tuners: 1, Buffer: 1 << 20, Linger: time.Second, ReadyTimeout: time.Second,
		OnError: func(reason string) { mu.Lock(); reasons = append(reasons, reason); mu.Unlock() },
	})
	t.Cleanup(r.Close)
	a, _ := watch(t, r, "1001", u.srv.URL+"/1001.m3u8")
	assert.Equal(t, http.StatusBadGateway, a.StatusCode)
	mu.Lock()
	assert.Equal(t, []string{relay.ReasonUnsupportedStream}, reasons)
	mu.Unlock()
	require.Eventually(t, func() bool { return r.InUse() == 0 }, time.Second, 10*time.Millisecond)
}

func TestARefusingUpstreamIsA502AndFreesTheTuner(t *testing.T) {
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusForbidden) }))
	t.Cleanup(refuse.Close)
	r := newRelay(t, 1, time.Second)
	a, _ := watch(t, r, "1001", refuse.URL+"/1001.ts")
	assert.Equal(t, http.StatusBadGateway, a.StatusCode)
	require.Eventually(t, func() bool { return r.InUse() == 0 }, time.Second, 10*time.Millisecond)
}

type panicky struct{ next http.RoundTripper }

func (p panicky) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/boom.ts") {
		panic("boom")
	}
	return p.next.RoundTrip(req)
}

func TestAPanickingUpstreamEndsOnlyItsStream(t *testing.T) {
	u := newUpstream(t)
	r := relay.New(relay.Options{
		HTTP: &http.Client{Transport: panicky{next: http.DefaultTransport}}, Tuners: 2, Buffer: 1 << 20,
		Linger: time.Second, ReadyTimeout: 2 * time.Second,
	})
	t.Cleanup(r.Close)
	good, _ := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	bad, _ := watch(t, r, "1002", u.srv.URL+"/boom.ts")
	assert.Equal(t, http.StatusBadGateway, bad.StatusCode)
	_, err := io.CopyN(io.Discard, good.Body, 1<<20)
	assert.NoError(t, err, "the other channel keeps streaming")
}

func TestAChannelChangeOnOneTunerTakesTheLingeringStreamsTuner(t *testing.T) {
	// A one-connection account (the owner's): leaving 1001 for 1002 must
	// not answer "all tuners in use" for the whole linger.
	u := newUpstream(t)
	r := newRelay(t, 1, 10*time.Second)
	a, cancel := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, _ = io.CopyN(io.Discard, a.Body, 1<<16)
	cancel()
	require.Eventually(t, func() bool { return r.Viewers() == 0 }, 2*time.Second, 10*time.Millisecond)
	b, _ := watch(t, r, "1002", u.srv.URL+"/1002.ts")
	assert.Equal(t, http.StatusOK, b.StatusCode, "the unwatched stream gives up its tuner")
	_, err := io.CopyN(io.Discard, b.Body, 1<<16)
	require.NoError(t, err)
	assert.Equal(t, 1, r.InUse())
}

func TestConfigureReachesStreamsStartedAfterIt(t *testing.T) {
	u := newUpstream(t)
	r := newRelay(t, 1, 10*time.Second)
	r.Configure(1, 1<<20, 0, "clustarr-configured")
	a, cancel := watch(t, r, "1001", u.srv.URL+"/1001.ts")
	_, _ = io.CopyN(io.Discard, a.Body, 1<<16)
	cancel()
	require.Eventually(t, func() bool { return r.InUse() == 0 }, 2*time.Second, 10*time.Millisecond, "a linger of 0 frees the tuner at once")
	assert.Equal(t, "clustarr-configured", u.userAgent.Load(), "the new user agent reaches the upstream")
}
