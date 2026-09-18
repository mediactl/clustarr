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

package cardigann

import (
	"net/http"
	"time"
)

// Session is what Engine.Login produces and every later Search/Download
// call on the same indexer carries forward.
type Session struct {
	Cookies []*http.Cookie
	// Headers is rare; most definitions authenticate via search.headers
	// instead (a Config value rendered into the request, not a Session).
	Headers   http.Header
	ExpiresAt time.Time
}

// LoginError signals that Engine.Login ran but the indexer rejected the
// credentials (an ErrorBlock matched the response).
type LoginError struct{ Message string }

func (e *LoginError) Error() string { return "cardigann: login failed: " + e.Message }
