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

package catalogue_test

import (
	"regexp"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/stretchr/testify/require"
)

// threeDPattern is the literal TRaSH "3D" custom format's ReleaseTitle regex
// (docs/research/quality.md §4.5): a variable-length lookbehind.
const threeDPattern = `(?<=\b[12]\d{3}\b).*\b(3d|sbs|half[.-]ou|half[.-]sbs)\b`

func TestKnownProblematicTRaSHPatternsRejectUnderStdlibRegexp(t *testing.T) {
	_, err := regexp.Compile(threeDPattern)
	require.Error(t, err, "stdlib regexp must reject lookbehind -- if this starts passing, Go's regexp package changed and the regexp2 dependency should be re-justified")
}

func TestKnownProblematicTRaSHPatternsCompileAndMatchUnderRegexp2(t *testing.T) {
	re, err := regexp2.Compile(threeDPattern, regexp2.IgnoreCase)
	require.NoError(t, err)
	re.MatchTimeout = 50 * time.Millisecond

	ok, err := re.MatchString("Movie.Title.2019.3D.1080p.BluRay.x264-GROUP")
	require.NoError(t, err)
	require.True(t, ok, "must match a release with a 4-digit year ahead of the 3D token")

	ok, err = re.MatchString("Movie.Title.3D.1080p.BluRay.x264-GROUP") // no year token before "3D"
	require.NoError(t, err)
	require.False(t, ok, "lookbehind requires a preceding 4-digit year")
}
