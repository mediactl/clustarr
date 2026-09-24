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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
)

const (
	// maxProbeBody caps what the FlareSolverr probe will read. Its index
	// endpoint answers with about a hundred bytes; the cap is the house rule
	// that every HTTP response body is read through one, so a proxy that
	// answers with a gigabyte does not become the operator's memory problem.
	maxProbeBody = 64 << 10

	// defaultRequestTimeout backs spec.requestTimeout up. The CRD defaults it
	// to "60s", so this only applies to an object built in code (a test, or a
	// future caller that skips the apiserver's defaulting).
	defaultRequestTimeout = 60 * time.Second
)

// errResponseTooLarge is returned when a probe response exceeds maxProbeBody.
var errResponseTooLarge = errors.New("indexerproxy: probe response too large")

// Prober answers "is this proxy reachable, and what version answered".
//
// It is injected so a test never needs a real proxy. The FlareSolverr client
// that solves challenges is app/indexer/proxy.FlareSolverr; this probe only asks
// the service whether it is up.
type Prober func(ctx context.Context, spec indexv1alpha1.IndexerProxySpec) (version string, err error)

// NewProber builds the production probe. A nil httpClient means
// http.DefaultClient and a nil dial means a plain net.Dialer.
//
// What it does is deliberately the least that answers the CRD's two status
// fields: a FlareSolverr is asked for its index document, which reports its
// version, and an HTTP or SOCKS proxy is dialled. It never sends a request
// THROUGH the proxy, never creates a FlareSolverr session and never uses
// spec.secretRef's credentials -- routing is app/indexer/proxy's.
func NewProber(httpClient *http.Client, dial func(ctx context.Context, network, address string) (net.Conn, error)) Prober {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	return func(ctx context.Context, spec indexv1alpha1.IndexerProxySpec) (string, error) {
		timeout := spec.RequestTimeout.Duration
		if timeout <= 0 {
			timeout = defaultRequestTimeout
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		addr := net.JoinHostPort(spec.Host, strconv.Itoa(int(spec.Port)))
		switch spec.Type {
		case indexv1alpha1.IndexerProxyTypeFlareSolverr:
			return probeFlareSolverr(ctx, httpClient, addr)
		case indexv1alpha1.IndexerProxyTypeHTTP,
			indexv1alpha1.IndexerProxyTypeSocks4,
			indexv1alpha1.IndexerProxyTypeSocks5:
			// A SOCKS or HTTP proxy reports no version, so status.version
			// stays whatever it was -- empty, here.
			return "", probeTCP(ctx, dial, addr)
		default:
			// Unreachable through the apiserver (spec.type carries an enum),
			// and validateSpec rejects it before the probe is called, so this
			// is the defence against a Prober invoked directly.
			return "", fmt.Errorf("indexerproxy: unknown proxy type %q", spec.Type)
		}
	}
}

// flareSolverrIndex is FlareSolverr's index document, which is what makes a
// version reportable at all: GET / answers
// {"msg":"FlareSolverr is ready!","version":"3.3.21","userAgent":"..."}.
type flareSolverrIndex struct {
	Msg     string `json:"msg"`
	Version string `json:"version"`
}

func probeFlareSolverr(ctx context.Context, c *http.Client, addr string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/", nil)
	if err != nil {
		return "", fmt.Errorf("indexerproxy: build probe request: %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("indexerproxy: probe %s: %w", addr, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("indexerproxy: probe %s: unexpected status %s", addr, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody+1))
	if err != nil {
		return "", fmt.Errorf("indexerproxy: read probe response: %w", err)
	}
	if len(body) > maxProbeBody {
		return "", fmt.Errorf("indexerproxy: probe %s: %w", addr, errResponseTooLarge)
	}

	var index flareSolverrIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return "", fmt.Errorf("indexerproxy: probe %s: not a FlareSolverr endpoint: %w", addr, err)
	}
	if index.Msg == "" && index.Version == "" {
		return "", fmt.Errorf("indexerproxy: probe %s: not a FlareSolverr endpoint: no msg or version", addr)
	}
	return index.Version, nil
}

func probeTCP(ctx context.Context, dial func(ctx context.Context, network, address string) (net.Conn, error), addr string) error {
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("indexerproxy: dial %s: %w", addr, err)
	}
	return conn.Close()
}
