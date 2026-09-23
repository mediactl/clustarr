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

package nntpstub_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/javi11/nzbparser"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/test/fixtures/nntpstub"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestBuildIsDeterministic(t *testing.T) {
	a := nntpstub.Build(700, 3)
	b := nntpstub.Build(700, 3)
	require.Equal(t, a.NZB, b.NZB, "two Build calls must produce byte-identical NZBs -- independent nntp-stub processes rely on this")
	require.Equal(t, a.Articles, b.Articles)
}

// TestFixtureNZBParsesWithTheRealParser is this fixture's own guard rail,
// the same discipline test/fixtures/torznabstub/server_test.go documents:
// the NZB is parsed here by the SAME library pkg/download/usenet/nzb.go
// parses with, so a shape mistake fails in `go test`, not deep into an e2e
// run.
func TestFixtureNZBParsesWithTheRealParser(t *testing.T) {
	fx := nntpstub.Build(0, 0)
	parsed, err := nzbparser.ParseWithOptions(bytes.NewReader(fx.NZB), nzbparser.ParseOptions{RemoveDuplicates: true})
	require.NoError(t, err)
	require.Len(t, parsed.Files, 1)
	require.Len(t, parsed.Files[0].Segments, nntpstub.DefaultSegmentCount)
	for i, seg := range parsed.Files[0].Segments {
		require.Equal(t, fx.Articles[i].ID, strings.Trim(seg.ID, "<>"))
		require.Equal(t, i+1, seg.Number)
	}
}

func TestNZBHandlerServesFixtureBody(t *testing.T) {
	fx := nntpstub.Build(1024, 2)
	srv := httptest.NewServer(nntpstub.NZBHandler(fx))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL) //nolint:noctx,gosec // httptest, loopback only
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, fx.NZB, body)
	require.Contains(t, resp.Header.Get("Content-Type"), "nzb")
}

// dial opens a raw NNTP connection to addr and reads the greeting line.
func dial(t *testing.T, addr string) (*textproto.Conn, string) {
	t.Helper()
	nc, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nc.Close() })
	tc := textproto.NewConn(nc)
	greeting, err := tc.ReadLine()
	require.NoError(t, err)
	return tc, greeting
}

func TestServerServesKnownArticlesAndDeniesConfiguredOnes(t *testing.T) {
	fx := nntpstub.Build(256, 3)
	denyID := fx.Articles[1].ID

	srv, err := nntpstub.NewServer(fx, nntpstub.Options{
		Addr:   "127.0.0.1:0",
		Deny:   map[string]int{denyID: 0}, // 0 -> DefaultDenyStatus (430)
		Logger: discardLogger(),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	tc, greeting := dial(t, srv.Addr())
	require.True(t, strings.HasPrefix(greeting, "200 "), "greeting: %q", greeting)

	// STAT on a served article: 223.
	require.NoError(t, tc.PrintfLine("STAT <%s>", fx.Articles[0].ID))
	line, err := tc.ReadLine()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "223 "), "stat: %q", line)

	// BODY on the denied article: the configured refusal code, not the
	// article's real content.
	require.NoError(t, tc.PrintfLine("BODY <%s>", denyID))
	line, err = tc.ReadLine()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "430 "), "denied body: %q", line)
	require.Equal(t, 1, srv.Denied(denyID))
	require.Equal(t, 0, srv.Served(denyID))

	// BODY on an unknown message-id: also 430, but never counted as a
	// deliberate denial -- it is simply not part of this fixture.
	require.NoError(t, tc.PrintfLine("BODY <does-not-exist@clustarr.fixture.test>"))
	line, err = tc.ReadLine()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "430 "), "unknown body: %q", line)

	require.NoError(t, tc.PrintfLine("QUIT"))
	line, err = tc.ReadLine()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "205 "), "quit: %q", line)
}

func TestAuthinfoRequiredWhenCredentialsConfigured(t *testing.T) {
	fx := nntpstub.Build(256, 1)
	srv, err := nntpstub.NewServer(fx, nntpstub.Options{
		Addr: "127.0.0.1:0", Username: "clustarr", Password: "secret", Logger: discardLogger(),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	tc, _ := dial(t, srv.Addr())

	require.NoError(t, tc.PrintfLine("BODY <%s>", fx.Articles[0].ID))
	line, err := tc.ReadLine()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "480 "), "unauthenticated body: %q", line)

	require.NoError(t, tc.PrintfLine("AUTHINFO USER clustarr"))
	line, err = tc.ReadLine()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "381 "), "authinfo user: %q", line)

	require.NoError(t, tc.PrintfLine("AUTHINFO PASS wrong"))
	line, err = tc.ReadLine()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "481 "), "wrong password: %q", line)

	require.NoError(t, tc.PrintfLine("AUTHINFO PASS secret"))
	line, err = tc.ReadLine()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "281 "), "right password: %q", line)

	require.NoError(t, tc.PrintfLine("BODY <%s>", fx.Articles[0].ID))
	line, err = tc.ReadLine()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(line, "222 "), "authenticated body: %q", line)
}

func TestRequestLogRecordsServedAndDenied(t *testing.T) {
	fx := nntpstub.Build(256, 2)
	denyID := fx.Articles[0].ID
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")

	srv, err := nntpstub.NewServer(fx, nntpstub.Options{
		Addr: "127.0.0.1:0", Deny: map[string]int{denyID: 0}, RequestLog: logPath, Logger: discardLogger(),
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	tc, _ := dial(t, srv.Addr())
	require.NoError(t, tc.PrintfLine("BODY <%s>", denyID))
	_, err = tc.ReadLine()
	require.NoError(t, err)
	require.NoError(t, tc.PrintfLine("BODY <%s>", fx.Articles[1].ID))
	_, err = tc.ReadLine()
	require.NoError(t, err)

	f, err := os.Open(logPath)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	require.NoError(t, sc.Err())
	require.Len(t, lines, 2)
	require.Contains(t, lines[0], `"action":"denied"`)
	require.Contains(t, lines[1], `"action":"served"`)
}

// TestCrossServerFailoverThroughTheRealClient is the reason this fixture
// exists: two independent Servers built from the SAME Fixture, one denying
// an article the other carries, driven through pkg/download/usenet's real
// production Client -- not this package's own protocol handling, and not
// pkg/download/usenet's in-process stub -- over real loopback TCP
// connections. If failover only worked in-process against a stub built
// specifically to prove it, this test would hang until its own deadline and
// fail, the same way a real two-provider cluster deployment would.
func TestCrossServerFailoverThroughTheRealClient(t *testing.T) {
	fx := nntpstub.Build(700, 3)
	deniedByPrimary := fx.Articles[1].ID

	primary, err := nntpstub.NewServer(fx, nntpstub.Options{
		Addr: "127.0.0.1:0", Deny: map[string]int{deniedByPrimary: 0}, Logger: discardLogger(),
	})
	require.NoError(t, err)
	backup, err := nntpstub.NewServer(fx, nntpstub.Options{Addr: "127.0.0.1:0", Logger: discardLogger()})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	var dones []chan struct{}
	for _, s := range []*nntpstub.Server{primary, backup} {
		done := make(chan struct{})
		dones = append(dones, done)
		go func(s *nntpstub.Server) { defer close(done); _ = s.Serve(ctx) }(s)
	}
	// cancel must run BEFORE waiting on the Serve goroutines, and
	// t.Cleanup runs LIFO -- so both belong in one cleanup, not two,
	// or the wait below deadlocks on a cancel that has not happened yet.
	t.Cleanup(func() {
		cancel()
		for _, d := range dones {
			<-d
		}
	})

	primaryHost, primaryPort := splitHostPort(t, primary.Addr())
	backupHost, backupPort := splitHostPort(t, backup.Addr())

	root := t.TempDir()
	c, err := usenet.New(usenet.Config{
		Providers: []usenet.Provider{
			{Name: "primary", Host: primaryHost, Port: primaryPort, Connections: 4, Priority: 1},
			{Name: "backup", Host: backupHost, Port: backupPort, Connections: 2, Priority: 1, Backup: true},
		},
		ScratchDir: filepath.Join(root, "scratch"),
		DataDir:    filepath.Join(root, "data"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	id, err := c.Add(context.Background(), download.AddRequest{Name: fx.Title, Payload: fx.NZB, Category: "movies"})
	require.NoError(t, err)

	deadline := time.Now().Add(20 * time.Second)
	var item download.Item
	for time.Now().Before(deadline) {
		item, err = c.Get(context.Background(), id)
		require.NoError(t, err)
		if item.Status == download.StatusCompleted || item.Status == download.StatusFailed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Equal(t, download.StatusCompleted, item.Status, "message: %s", item.Message)

	var want []byte
	for _, a := range fx.Articles {
		want = append(want, a.Data...)
	}
	got, err := os.ReadFile(filepath.Join(root, "data", "movies", fx.Title, nntpstub.FileName))
	require.NoError(t, err)
	require.Equal(t, want, got, "the assembled file must be byte-identical to what the fixture posted")

	// The proof, on the wire: the primary refused the article and never
	// served it; the backup is the one that did.
	require.Equal(t, 1, primary.Denied(deniedByPrimary))
	require.Equal(t, 0, primary.Served(deniedByPrimary))
	require.GreaterOrEqual(t, backup.Served(deniedByPrimary), 1,
		"the refused article must have been fetched from the backup -- that is the failover")
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}
