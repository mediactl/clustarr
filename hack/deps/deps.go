//go:build tools

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

// Package deps exists only to keep `go mod tidy` from removing modules that
// are pre-added for a later task but not yet imported by real code. Delete an
// entry the moment a real importer lands -- an entry that outlives its task is
// a dependency nobody can account for.
//
// Remaining after Phase B (2026-09-18): mimetype (Phase C file classification),
// sprig (Phase G, if the Cardigann template surface needs it), x/net/proxy
// (Phase G IndexerProxy SOCKS dialing). Prune each when its importer lands.
package deps

import (
	_ "github.com/Masterminds/sprig/v3"
	_ "github.com/gabriel-vasile/mimetype"
	_ "golang.org/x/net/proxy"
)
