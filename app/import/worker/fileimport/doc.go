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

// Package fileimport is the import agent's completed-download worker on
// importarr-fileimport (ADR-0019 §6.9): the two halves of an import that
// touch the payload and the library, while app/import/importplan decides
// in the manager.
//
//   - The inspect (ImportInspectTask, inspect.go) reads a completed
//     transfer's payload and says what each file is: its name parsed, its
//     probe summarised (the probe corrects the name's quality before
//     anything judges it), its size class (a suspected sample), the item it
//     maps to (episodes and scene numbering by MatchEpisodes, the one rule
//     the rescan shares), the import fields it freezes and the library path
//     pkg/naming renders for it. Every refusal carries its class. The
//     answer is the clustarr-imports record at RecordSubKey(entry,
//     "inspect").
//   - The execute (ImportExecuteTask, execute.go) carries out the plan the
//     manager approved: it re-reads every MediaFile the plan replaces
//     through the APIReader and refuses a stale plan, checks free space,
//     places each file (placeFile: strictly under the root folder, an
//     existing destination recycle-linked first, never an overwrite),
//     recycles the replaced files and places an audio donor. The answer is
//     the record at RecordSubKey(entry, "execute").
//
// Neither writes a Kubernetes object: the manager materialises the
// MediaFiles a placement names (spec under importarr-worker), deletes the
// replaced ones with UID preconditions and applies a donor's AudioGraft.
// The scanner-never-guesses rule holds in its import shape: a file the
// inspect cannot attribute is a rejection naming why, never a speculative
// MediaFile. A task whose record already holds its seq or a later one is
// superseded and dropped, so a redelivery does nothing twice; a v1
// ImportTask is discarded.
//
// The recycle-bin sweep (RecycleSweeper, importarr-recycle) lives here too,
// beside the placement it cleans up after.
package fileimport
