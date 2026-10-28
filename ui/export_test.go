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

package ui

import "time"

// SetNowForTest sets the clock the Live TV pages read.
func SetNowForTest(s *Server, now func() time.Time) { s.now = now }

// SetAddSearchTimeoutForTest shortens Add New's search deadline so a test
// of the "not responding" answer does not wait the real 10 s.
func SetAddSearchTimeoutForTest(s *Server, d time.Duration) { s.addSearchTimeout = d }

// ArtIndexSyncedForTest reports whether s's artwork index has finished its
// replay, so a test can make /art choose from the index rather than the
// store.
func ArtIndexSyncedForTest(s *Server) bool { return s.artIndex != nil && s.artIndex.Synced() }

// SetArtReadTimeoutForTest shortens /art's whole-object read deadline for
// the test's lifetime, so the overwritten-mid-read case (research E14)
// stalls for seconds rather than the real ten.
func SetArtReadTimeoutForTest(t interface{ Cleanup(func()) }, d time.Duration) {
	prev := artReadTimeout
	artReadTimeout = d
	t.Cleanup(func() { artReadTimeout = prev })
}
