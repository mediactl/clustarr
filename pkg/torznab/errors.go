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

package torznab

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrorCode is a Newznab/Torznab <error code="N"/> value (§4.5).
type ErrorCode int

// Newznab/Torznab error codes (docs/research/indexers.md §4.5).
const (
	ErrIncorrectCredentials   ErrorCode = 100
	ErrAccountSuspended       ErrorCode = 101
	ErrInsufficientPrivileges ErrorCode = 102
	ErrRegistrationDenied     ErrorCode = 103
	ErrRegistrationsClosed    ErrorCode = 104
	ErrMissingParameter       ErrorCode = 200
	ErrIncorrectParameter     ErrorCode = 201
	ErrNoSuchFunction         ErrorCode = 202
	ErrFunctionNotAvailable   ErrorCode = 203
	ErrNoSuchItem             ErrorCode = 300
	ErrRequestLimitReached    ErrorCode = 500 // Torznab-specific
	ErrDownloadLimitReached   ErrorCode = 501 // Torznab-specific
	ErrUnknown                ErrorCode = 900
	ErrAPIDisabled            ErrorCode = 910
)

// Error is either a parsed <error code=".." description=".."/> body (HTTP
// 200) or a Prowlarr-style HTTP-level failure (410 disabled, 429 rate
// limited) that never had an XML body at all -- HTTPStatus disambiguates.
type Error struct {
	Code        ErrorCode // 0 when the failure was HTTP-level only
	Description string
	HTTPStatus  int
	RetryAfter  time.Duration // set from the Retry-After header on 429
}

func (e *Error) Error() string {
	return fmt.Sprintf("torznab: %s (code %d, http %d)", e.Description, e.Code, e.HTTPStatus)
}

// wireError mirrors a standalone <error> document.
type wireError struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

// ParseError parses r as an <error> document. It returns (nil, nil) -- not
// an error -- when r is well-formed XML but is not an <error> element, so
// callers can try ParseError before falling back to ParseCaps/ParseResults
// on a document of unknown shape.
func ParseError(r io.Reader) (*Error, error) {
	dec := xml.NewDecoder(r)

	var root xml.StartElement
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			root = se
			break
		}
	}

	if root.Name.Local != "error" {
		return nil, nil
	}

	var we wireError
	if err := dec.DecodeElement(&we, &root); err != nil {
		return nil, err
	}

	return &Error{Code: ErrorCode(we.Code), Description: we.Description}, nil
}
