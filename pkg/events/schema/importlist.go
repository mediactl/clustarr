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

package schema

import "time"

// ListSnapshot is the full listed set of one import list's sync, written by
// the import agent into clustarr-importlist at snapshot.<KVKeyToken(list
// uid)> (chunks snapshot.<token>.<n> past 512 KiB) (ADR-0019 §7.6, ruling
// R17). The ImportList controller diffs and applies it; the agent decides
// nothing.
type ListSnapshot struct {
	List     ItemRef   `json:"list"`
	SyncedAt time.Time `json:"syncedAt"`
	Error    string    `json:"error,omitempty"`
	// NeedsToken is a sync refused for an expired or revoked token: the
	// controller refreshes it and re-issues the sync.
	NeedsToken bool `json:"needsToken,omitempty"`
	// Chunk is this value's index, Chunks how many make the snapshot.
	Chunk  int                `json:"chunk,omitempty"`
	Chunks int                `json:"chunks,omitempty"`
	Kinds  []ListKindSnapshot `json:"kinds,omitempty"`
}

// Schema implements Payload.
func (ListSnapshot) Schema() string { return "importarr.ListSnapshot.v1" }

// ListKindSnapshot is one kind's part of a list's snapshot.
type ListKindSnapshot struct {
	Kind    string       `json:"kind"`
	Fetched int32        `json:"fetched,omitempty"`
	Items   []ListedItem `json:"items,omitempty"`
}

// ListedItem is one item a list names.
type ListedItem struct {
	IDs        map[string]string `json:"ids,omitempty"`
	Title      string            `json:"title,omitempty"`
	Year       int32             `json:"year,omitempty"`
	ResolvedID int64             `json:"resolvedID,omitempty"`
	Unresolved bool              `json:"unresolved,omitempty"`
}

// RecycleFilesTask asks the import domain to move named files into a
// RootFolder's recycle bin: what a list removal or an orphan part leaves
// behind (ADR-0019 §7.6). Subject events.SubjectImportRecycleFiles,
// consumer importarr-recycle.
type RecycleFilesTask struct {
	RootFolder Ref      `json:"rootFolder"`
	Paths      []string `json:"paths"`
}

// Schema implements Payload.
func (RecycleFilesTask) Schema() string { return "importarr.RecycleFilesTask.v1" }
