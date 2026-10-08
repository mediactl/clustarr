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

package agent

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/relindex"
)

// TestTheIndexReadinessCheckFailsOnAClosedStore proves the SQLite gate is a
// real check and not a constant nil. A checker that cannot fail is the same
// as no checker, and it is the shape that ships when nobody tries it.
func TestTheIndexReadinessCheckFailsOnAClosedStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "releases.db")
	store, closer, err := relindex.Open(t.Context(), path)
	require.NoError(t, err)

	check := IndexReadyChecker(store)
	require.NoError(t, check(&http.Request{}))

	require.NoError(t, closer.Close())
	require.Error(t, check(&http.Request{}),
		"the readiness check passed against a closed SQLite handle, so it would never take an "+
			"index pod with a broken index out of service")
}
