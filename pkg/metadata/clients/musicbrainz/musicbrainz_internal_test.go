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

package musicbrainz

import (
	"errors"
	"net/http"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	mb "go.uploadedlobster.com/musicbrainzws2"

	"github.com/mediactl/clustarr/pkg/metadata"
)

// TestMapError pins every branch of mapError. The StatusCode-0 rows are the
// ones musicbrainzws2 cannot tell apart by itself (see the package doc):
// only the recorded outcome distinguishes a decode failure from a transport
// failure. The 429/503 rows cannot be driven through the real library in a
// unit test without waiting out its own five retries.
func TestMapError(t *testing.T) {
	outcome := func(status int, err error) *callOutcome {
		o := &callOutcome{}
		var resp *http.Response
		if status != 0 {
			resp = &http.Response{StatusCode: status}
		}
		o.record(resp, err)
		return o
	}
	flattened := &mb.ClientError{StatusCode: 0, Message: "flattened by the library"}

	tests := []struct {
		name     string
		err      error
		outcome  *callOutcome
		is       error
		isNot    error
		rateLimd bool
	}{
		{name: "404", err: &mb.ClientError{StatusCode: 404}, outcome: outcome(404, nil), is: metadata.ErrNotFound},
		{name: "401", err: &mb.ClientError{StatusCode: 401}, outcome: outcome(401, nil), is: metadata.ErrAuth},
		{name: "403", err: &mb.ClientError{StatusCode: 403}, outcome: outcome(403, nil), is: metadata.ErrAuth},
		{name: "429", err: &mb.ClientError{StatusCode: 429}, outcome: outcome(429, nil), is: metadata.ErrRateLimited, rateLimd: true},
		{name: "503 is MusicBrainz's rate-limit answer", err: &mb.ClientError{StatusCode: 503}, outcome: outcome(503, nil), is: metadata.ErrRateLimited, rateLimd: true},
		{name: "500", err: &mb.ClientError{StatusCode: 500}, outcome: outcome(500, nil), isNot: metadata.ErrDecode},
		{name: "decode failure on a 200", err: flattened, outcome: outcome(200, nil), is: metadata.ErrDecode},
		{name: "connection refused", err: flattened, outcome: outcome(0, syscall.ECONNREFUSED), is: syscall.ECONNREFUSED, isNot: metadata.ErrDecode},
		{name: "oversized body", err: flattened, outcome: outcome(0, metadata.ErrResponseTooLarge), is: metadata.ErrResponseTooLarge, isNot: metadata.ErrDecode},
		{name: "no round trip at all", err: errors.New("building the request failed"), outcome: &callOutcome{}, isNot: metadata.ErrDecode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapError(tt.err, tt.outcome)
			require.Error(t, got)
			if tt.is != nil {
				require.ErrorIs(t, got, tt.is)
			}
			if tt.isNot != nil {
				require.NotErrorIs(t, got, tt.isNot)
			}
			if tt.rateLimd {
				var rl *metadata.RateLimitedError
				require.ErrorAs(t, got, &rl)
			}
		})
	}
}
