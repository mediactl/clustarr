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

// Package trakt is a pkg/importlist provider for Trakt: the device-code
// OAuth flow (device.go) and watchlist/collection/list/trending/popular
// fetch (list.go).
package trakt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxErrorBodyBytes bounds how much of a non-2xx response body doJSON
// reads for APIError.Body, so a misbehaving server returning an unbounded
// stream can't make an error path block on an unbounded read.
const maxErrorBodyBytes = 4 * 1024

// DefaultBaseURL is the production Trakt API endpoint.
const DefaultBaseURL = "https://api.trakt.tv"

// Credentials is a Trakt application's client ID and secret.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// APIError is returned when Trakt responds with an unexpected status code.
type APIError struct {
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *APIError) Error() string {
	return fmt.Sprintf("trakt: unexpected status %d: %s", e.StatusCode, e.Body)
}

// Option configures a DeviceFlow or a List.
type Option func(*options)

type options struct {
	baseURL string
	client  *http.Client
}

// WithBaseURL overrides the Trakt API base URL, for tests.
func WithBaseURL(u string) Option { return func(o *options) { o.baseURL = u } }

// WithHTTPClient overrides the *http.Client used for requests.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

func newOptions(opts []Option) options {
	o := options{baseURL: DefaultBaseURL, client: http.DefaultClient}
	for _, apply := range opts {
		apply(&o)
	}
	return o
}

// doJSON executes req and, when the response is 200 OK and out is non-nil,
// decodes the JSON body into out. For any other status code it instead
// reads up to maxErrorBodyBytes of the response body and returns it as
// body, so a caller building an APIError can report what the server
// actually said. The caller is responsible for handling any other status
// code from the returned response; doJSON never treats a non-200 status as
// an error itself, since the callers in this package (device-flow polling
// in particular) attach meaning to specific non-200 codes.
func doJSON(ctx context.Context, client *http.Client, req *http.Request, out any) (resp *http.Response, body []byte, err error) {
	req = req.WithContext(ctx)
	resp, err = client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return resp, nil, fmt.Errorf("trakt: decode response: %w", err)
			}
		}
		return resp, nil, nil
	}

	body, _ = io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return resp, body, nil
}
