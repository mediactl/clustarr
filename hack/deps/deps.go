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
// Remaining after Phase C (2026-09-19):
//   - mimetype   -- Phase C file classification.
//   - sprig      -- Phase G, if the Cardigann template surface needs it.
//   - x/net/proxy -- Phase G IndexerProxy SOCKS dialing.
//
// Added by D2-0 (2026-09-22) for Phase D2. Every version below was resolved
// and its licence read on that date; the full record, including why the cgo
// NNTP pool the research note recommends is NOT here, is in
// .superpowers/sdd/2026-09-22-phase-d2-grabarr/task-D2-0-report.md.
//
//   - anacrolix/torrent v1.61.0 (MPL-2.0) -- the BitTorrent engine.
//     Task D2-1 retires this entry.
//   - javi11/nzbparser v0.5.5 (MIT) -- NZB parsing.
//     Task D2-2 retires this entry.
//   - Tensai75/nntp v0.1.5 (BSD-3) -- the RFC 3977 client one connection is
//     built on. grabarr's pool, failover and quota accounting are ours,
//     because the maintained pool (javi11/nntppool/v4) is cgo-only and
//     cmd/clustarr is one binary that images/Dockerfile.controller builds
//     with CGO_ENABLED=0 -- the same constraint ADR-0003 Ruling R8 already
//     settled for the SQLite driver.
//     Task D2-2 retires this entry.
//   - javi11/rapidyenc (MIT), pseudo-version v0.0.0-20260215144528-f0dac5a39d34
//     -- yEnc decode in pure Go plus hand-written amd64/arm64 assembly, so it
//     links under CGO_ENABLED=0. Deliberately NOT mnightingale/rapidyenc,
//     which is the same code behind cgo.
//     Task D2-2 retires this entry.
//   - nwaples/rardecode/v2 v2.4.1 (BSD-2) -- RAR5 extraction, multi-volume.
//     Task D2-2 retires this entry.
//   - bodgit/sevenzip v1.6.5 (BSD-3) -- 7z extraction.
//     Task D2-2 retires this entry.
//
// PAR2 verify and repair deliberately add no module. The research note
// measures akalin/gopar as too slow and too memory-hungry for a 50 GB release,
// and images/Dockerfile.media already ships par2cmdline-turbo v1.5.0, which
// D2-6 execs.
//
// Two corrections to earlier versions of this comment:
//
// The version before Phase C claimed cron was pre-added here for Tasks C9 and
// C10. It never was -- it is absent from go.mod, from go.sum and from the
// import block below -- and C10 wrote its own Vixie-cron parser instead, which
// review upheld over adopting robfig. A keeper file that lists a module it does
// not keep is worse than no comment.
//
// The version before this one still kept sqlite for "Phase D1 Task D1-2's
// release index", with D1-10 named as the owner of its deletion. pkg/relindex
// imports the driver for real now (store.go, and two tests), so the entry had
// already outlived its task: it is removed here rather than left for D1-10,
// because the rule at the top of this comment is "the moment a real importer
// lands", and a keeper entry for a module that is genuinely required is the
// same defect as one for a module that is not.
package deps

import (
	_ "github.com/Masterminds/sprig/v3"
	_ "github.com/Tensai75/nntp"
	_ "github.com/anacrolix/torrent"
	_ "github.com/anacrolix/torrent/storage"
	_ "github.com/bodgit/sevenzip"
	_ "github.com/gabriel-vasile/mimetype"
	_ "github.com/javi11/nzbparser"
	_ "github.com/javi11/rapidyenc"
	_ "github.com/nwaples/rardecode/v2"
	_ "golang.org/x/net/proxy"
)
