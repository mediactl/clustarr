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

// Package relindex is the local release index: every release indexarr sees,
// from an RSS poll or a federated search, in a SQLite database with an FTS5
// virtual table over the normalised title and the release group.
//
// It is a cache, not a system of record (ADR-0003). Losing the file costs a
// re-sync and nothing else, which is what makes a single-writer, single-volume
// design acceptable and why there are no backups.
//
// # Shape
//
// One content table, `releases`, keyed UNIQUE(indexer, guid); one external-
// content FTS5 table, `releases_fts`, over `title_norm` and `grp`, kept in
// sync by three triggers. The engine is SQLite through the pure-Go
// modernc.org/sqlite driver: the indexarr image is distroless-static and a cgo
// driver will not link (ADR-0003, Ruling R8).
//
// # Deployment
//
// The file lives on the RWO PVC `clustarr-index`, mounted at
// /var/lib/clustarr/index, at /var/lib/clustarr/index/releases.db. indexarr is
// pinned to exactly one replica with a Recreate strategy precisely so there is
// exactly one writer process; SQLite plus WAL is safe for one writer and many
// readers, and is not safe for two pods. This package takes the path as an
// argument and hardcodes nothing.
//
// # What this package does not do
//
// It does not normalise titles -- the caller supplies Release.TitleNorm and
// must normalise Query.Text with the same function, or nothing will match.
// pkg/release.TitleNorm is the function built for this: it keeps letters in
// every script, where release.CleanTitle keeps only ASCII ones
// (titlenorm_test.go runs the round trip through a real index). It
// does not schedule the retention sweep -- Prune is a pure function of its
// olderThan argument and Open starts no goroutines. It does not know what is
// inside Release.InfoJSON. It does not invent a query limit.
package relindex
