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

package relindex_test

import (
	"testing"

	"github.com/mediactl/clustarr/pkg/relindex/storetest"
)

// TestSQLiteStoreContract runs the full Store behavioural contract
// (storetest.Run) against the SQLite engine. The behavioural test bodies
// live in pkg/relindex/storetest so the Postgres store
// (postgres_test.go's TestPostgresStoreContract) is held to the exact same
// assertions. SQLite-only tests -- WAL mode, DSN parsing, the user_version
// stamp, ExportedForTestDB -- stay in this package's other *_test.go files.
func TestSQLiteStoreContract(t *testing.T) {
	storetest.Run(t, newStore)
}
