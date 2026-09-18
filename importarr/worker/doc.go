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

// Package worker will hold importarr's queue consumers: work.importarr.scan,
// work.importarr.list and work.importarr.fileimport (amendment §A1.6). They
// run on every replica of the scalable `importarr-worker` Deployment,
// selected by [github.com/mediactl/clustarr/importarr.RoleWorker], and mount
// the same RWX /data volume the controllers do.
//
// A scan of a large library is chunked by directory so one message is not a
// multi-hour unit of work, and the scanner never guesses: an unattributable
// file goes to LibraryScan.status.unmatched with the reason, never a
// speculative item.
//
// Empty for M0/Phase A; the consumers land in M1.
package worker
