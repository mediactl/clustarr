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

package indexerproxy

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
)

// hostPort splits a listener address into the CRD's two fields.
func hostPort(t *testing.T, addr string) (string, int32) {
	t.Helper()
	h, p, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	n, err := strconv.Atoi(p)
	require.NoError(t, err)
	return h, int32(n)
}

func flareSolverr(t *testing.T, handler http.HandlerFunc) (indexv1alpha1.IndexerProxySpec, *http.Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	host, port := hostPort(t, srv.Listener.Addr().String())
	return indexv1alpha1.IndexerProxySpec{
		Type: indexv1alpha1.IndexerProxyTypeFlareSolverr, Host: host, Port: port,
		RequestTimeout: metav1.Duration{Duration: 5 * time.Second},
	}, srv.Client()
}

func TestProbeFlareSolverrReportsItsVersion(t *testing.T) {
	spec, c := flareSolverr(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"msg":"FlareSolverr is ready!","version":"3.3.21","userAgent":"Mozilla"}`))
	})
	version, err := NewProber(c, nil)(context.Background(), spec)
	require.NoError(t, err)
	assert.Equal(t, "3.3.21", version)
}

func TestProbeFlareSolverrRejectsANonFlareSolverrEndpoint(t *testing.T) {
	spec, c := flareSolverr(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>hello</html>`))
	})
	_, err := NewProber(c, nil)(context.Background(), spec)
	require.ErrorContains(t, err, "not a FlareSolverr endpoint")
}

func TestProbeFlareSolverrRejectsANon200(t *testing.T) {
	spec, c := flareSolverr(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := NewProber(c, nil)(context.Background(), spec)
	require.ErrorContains(t, err, "unexpected status")
}

// Every HTTP response body in this project is read through a cap.
func TestProbeFlareSolverrCapsTheResponseBody(t *testing.T) {
	spec, c := flareSolverr(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"msg":"` + strings.Repeat("a", maxProbeBody+100) + `"}`))
	})
	_, err := NewProber(c, nil)(context.Background(), spec)
	require.ErrorIs(t, err, errResponseTooLarge)
}

func TestProbeDialsASocksProxyAndReportsNoVersion(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	host, port := hostPort(t, ln.Addr().String())
	spec := indexv1alpha1.IndexerProxySpec{
		Type: indexv1alpha1.IndexerProxyTypeSocks5, Host: host, Port: port,
		RequestTimeout: metav1.Duration{Duration: 5 * time.Second},
	}
	version, err := NewProber(nil, nil)(context.Background(), spec)
	require.NoError(t, err)
	assert.Empty(t, version, "a SOCKS proxy reports no version")
}

func TestProbeReportsARefusedConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	host, port := hostPort(t, ln.Addr().String())
	require.NoError(t, ln.Close()) // nothing is listening there now

	spec := indexv1alpha1.IndexerProxySpec{
		Type: indexv1alpha1.IndexerProxyTypeHTTP, Host: host, Port: port,
		RequestTimeout: metav1.Duration{Duration: 2 * time.Second},
	}
	_, err = NewProber(nil, nil)(context.Background(), spec)
	require.ErrorContains(t, err, "dial")
}

func TestProbeRejectsAnUnknownType(t *testing.T) {
	_, err := NewProber(nil, nil)(context.Background(), indexv1alpha1.IndexerProxySpec{
		Type: "carrier-pigeon", Host: "h", Port: 1,
	})
	require.ErrorContains(t, err, "unknown proxy type")
}

func TestValidateSpec(t *testing.T) {
	ok := indexv1alpha1.IndexerProxySpec{Type: indexv1alpha1.IndexerProxyTypeHTTP, Host: "h", Port: 8080}
	require.NoError(t, validateSpec(ok))

	noHost := ok
	noHost.Host = ""
	require.ErrorContains(t, validateSpec(noHost), "spec.host")

	noPort := ok
	noPort.Port = 0
	require.ErrorContains(t, validateSpec(noPort), "spec.port",
		"an absent port is reported, never guessed at")

	badPort := ok
	badPort.Port = 70000
	require.ErrorContains(t, validateSpec(badPort), "spec.port")

	badType := ok
	badType.Type = "carrier-pigeon"
	require.ErrorContains(t, validateSpec(badType), "spec.type")
}

// probeStateFrom is what makes the complete-declaration rule possible on a
// path that cannot probe: it re-sends what is already there.
func TestProbeStateFromSeedsEveryOwnedField(t *testing.T) {
	now := metav1.Now()
	got := probeStateFrom(indexv1alpha1.IndexerProxyStatus{LastCheckedAt: &now, Version: "3.3.21"})
	assert.Equal(t, probeState{LastCheckedAt: &now, Version: "3.3.21"}, got,
		"a field missing here is a field both early returns release")
}
