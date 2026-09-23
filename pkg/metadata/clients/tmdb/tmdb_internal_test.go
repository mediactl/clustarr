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

package tmdb

import (
	"testing"

	rawtmdb "github.com/cyruzin/golang-tmdb"
	"github.com/stretchr/testify/require"
)

// TestLibraryBaseURLMatchesGolangTMDB: baseURLTransport rewrites requests
// that start with libraryBaseURL. If a golang-tmdb upgrade changed its
// default, every custom base URL would silently stop applying and requests
// would go to the real TMDB -- this is the tripwire. Nothing in this module
// calls SetCustomBaseURL/SetAlternateBaseURL, so the global is the default.
func TestLibraryBaseURLMatchesGolangTMDB(t *testing.T) {
	raw, err := rawtmdb.Init("k")
	require.NoError(t, err)
	require.Equal(t, libraryBaseURL, raw.GetBaseURL())
}
