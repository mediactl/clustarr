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

package torznab_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestParseErrorXMLBody(t *testing.T) {
	f, err := os.Open("../../test/data/torznab/error.xml")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	e, err := torznab.ParseError(f)
	require.NoError(t, err)
	require.NotNil(t, e)
	require.Equal(t, torznab.ErrRequestLimitReached, e.Code)
	require.Equal(t, "Request limit reached", e.Description)
	require.Equal(t, 0, e.HTTPStatus, "an XML-body error has no HTTP-level status of its own yet")
}

func TestParseErrorReturnsNilNilForANonErrorDocument(t *testing.T) {
	f, err := os.Open("../../test/data/torznab/caps.xml")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	e, err := torznab.ParseError(f)
	require.NoError(t, err)
	require.Nil(t, e)
}

func TestParseErrorMalformedInputNeverPanics(t *testing.T) {
	cases := map[string]string{
		"garbage":   "not xml at all {{{",
		"truncated": `<?xml version="1.0"?><error code="5`,
		"empty":     "",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			require.NotPanics(t, func() {
				_, _ = torznab.ParseError(strings.NewReader(body))
			})
		})
	}
}

func TestErrorErrorString(t *testing.T) {
	e := &torznab.Error{Code: torznab.ErrRequestLimitReached, Description: "Request limit reached", HTTPStatus: 200}
	require.Contains(t, e.Error(), "Request limit reached")
}
