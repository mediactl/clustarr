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

package catalogue

import (
	"context"
	"time"

	"github.com/dlclark/regexp2"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// trashMatchTimeout is spec §9's fixed 50ms budget: "MatchTimeout 50 ms;
// timeout = no match + log".
const trashMatchTimeout = 50 * time.Millisecond

// compileTRaSH compiles a TRaSH ReleaseTitle/ReleaseGroup pattern the way
// *arr does: case-insensitive, backtracking (.NET-compatible) regex, with a
// timeout so a catastrophic pattern degrades to "no match" instead of
// hanging a reconcile or search worker.
func compileTRaSH(pattern string) (*regexp2.Regexp, error) {
	re, err := regexp2.Compile(pattern, regexp2.IgnoreCase)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = trashMatchTimeout
	return re, nil
}

// matchTRaSH runs re against s, treating a timeout as "no match" rather than
// an error, and logging the timeout through ctx so an operator can find the
// pathological pattern without the caller having to thread an error return
// through every Condition kind.
func matchTRaSH(ctx context.Context, re *regexp2.Regexp, name, s string) bool {
	ok, err := re.MatchString(s)
	if err != nil {
		logging.FromContext(ctx).Warn("regexp2 match timed out", "condition", name, "timeout", trashMatchTimeout)
		return false
	}
	return ok
}

// MatchTRaSHForTest exposes matchTRaSH to tests in catalogue_test (external
// test package); production callers never need it directly, only through
// Match/Score.
func MatchTRaSHForTest(ctx context.Context, re *regexp2.Regexp, name, s string) bool {
	return matchTRaSH(ctx, re, name, s)
}
