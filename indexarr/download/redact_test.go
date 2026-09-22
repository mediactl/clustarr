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

package download

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/cardigann"
)

func TestRedactRawURLDropsEverythingSecret(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"passkey in query", "https://tr.example/dl?id=7&passkey=deadbeef", "https://tr.example/dl"},
		{"apikey in query", "https://nzb.example/api?t=get&apikey=abc123", "https://nzb.example/api"},
		{"userinfo", "https://user:hunter2@tr.example/dl", "https://tr.example/dl"},
		{"fragment", "https://tr.example/dl#frag", "https://tr.example/dl"},
		{"unparseable keeps the prefix", "ht tp://x/y?passkey=s3cret", "ht tp://x/y"},
		{"no query is unchanged", "https://tr.example/dl", "https://tr.example/dl"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, redactRawURL(tc.in))
			require.NotContains(t, redactRawURL(tc.in), "passkey")
			require.NotContains(t, redactRawURL(tc.in), "hunter2")
		})
	}
}

func TestRedactErrKeepsTheCauseAndDropsTheURL(t *testing.T) {
	ue := &url.Error{Op: "Get", URL: "https://tr.example/dl?passkey=s3cret", Err: context.Canceled}
	got := cardigann.RedactErr(ue)
	require.NotContains(t, got.Error(), "s3cret")
	require.ErrorIs(t, got, context.Canceled, "errors.Is must still see through it")

	plain := errors.New("boom")
	require.Equal(t, plain, cardigann.RedactErr(plain))
	require.NoError(t, cardigann.RedactErr(nil))
}

func TestScrubReplacesSecretValuesButNotShortOnes(t *testing.T) {
	s := scrubber([]string{"deadbeefcafe", "abc", ""})
	require.Equal(t, "auth failed for ***", s("auth failed for deadbeefcafe"))
	// "abc" is too short to scrub: it would redact ordinary prose.
	require.Equal(t, "abc problem", s("abc problem"))
	require.Equal(t, "", s(""))
}

func TestTruncateBoundsAnAttackerControlledString(t *testing.T) {
	require.Equal(t, "abc", truncate("abc", 8))
	require.Equal(t, "abcde...", truncate("abcdefghij", 8))
	require.Len(t, truncate(strings.Repeat("x", 10_000), maxErrorChars), maxErrorChars)
}
