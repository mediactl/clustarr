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
// It keeps nothing (gap fixes, W3 dependency prune, 2026-09-23). The last
// entries went two ways:
//
//   - Real importers had landed, so the entries were redundant:
//     anacrolix/torrent and anacrolix/torrent/storage (pkg/download/torrent),
//     Tensai75/nntp, javi11/nzbparser, javi11/rapidyenc, nwaples/rardecode/v2
//     and bodgit/sevenzip (pkg/download/usenet). These modules stay in go.mod
//     as the real code requires them.
//   - Nothing ever imported them, so they left go.mod: Masterminds/sprig/v3
//     (the Cardigann template surface never needed it), gabriel-vasile/
//     mimetype (pkg/fsops classifies by extension), golang.org/x/net/proxy
//     (app/indexer/proxy dials SOCKS4 itself and SOCKS5 through net/http), and
//     antchfx/xmlquery with its antchfx/xpath dependency, unimported since
//     gap fix X8a queried XML definitions with CSS, as Prowlarr does.
//
// Add an entry here only for a module a planned task will import, and name
// the task that retires it.
//
// PAR2 verify and repair deliberately add no module. The research note
// measures akalin/gopar as too slow and too memory-hungry for a 50 GB release,
// and images/Dockerfile.media already ships par2cmdline-turbo v1.5.0, which
// pkg/download/usenet execs. The full D2-0 record, including why the cgo NNTP
// pool the research note recommends was never added (javi11/nntppool/v4 is
// cgo-only and cmd/clustarr builds with CGO_ENABLED=0 for the controller
// image), is in .superpowers/sdd/2026-09-22-phase-d2-grabarr/task-D2-0-report.md.
//
// Two corrections to earlier versions of this comment, kept as history:
//
// The version before Phase C claimed cron was pre-added here for Tasks C9 and
// C10. It never was -- it was absent from go.mod, from go.sum and from the
// import block -- and C10 wrote its own Vixie-cron parser instead, which
// review upheld over adopting robfig. A keeper file that lists a module it does
// not keep is worse than no comment.
//
// A later version still kept sqlite for "Phase D1 Task D1-2's release index",
// with D1-10 named as the owner of its deletion. pkg/relindex imports the
// driver for real, so the entry had already outlived its task and was removed
// then, because the rule at the top of this comment is "the moment a real
// importer lands".
//
// M7's pgx/v5 and embedded-postgres entries, pre-added in W0-1 for Task A1,
// were retired by A1 itself: pkg/relindex/postgres.go imports
// github.com/jackc/pgx/v5/stdlib for real (OpenPostgres, spec §A.1) and
// pkg/relindex/postgres_test.go imports github.com/fergusstrange/
// embedded-postgres for real (TestPostgresStoreContract, spec §A.2).
// x/image stays -- C2 (pkg/overlay) has not landed yet.
package deps

// M7 (plan docs/superpowers/plans/2026-09-24-index-artwork-ratings-plex.md)
// pre-adds x/image in W0-1; it is retired by the task that imports it for
// real: x/image by C2 (pkg/overlay).
import (
	_ "golang.org/x/image/draw"
)
