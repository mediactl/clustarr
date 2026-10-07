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

// Package metadatainputs is what a metadata refresh is asked for and how its
// answer is matched to the ask (ADR-0019 §7.1, A5.1): the inputs of an
// item's MetadataTask v2 (its provider ids, the document schema version the
// manager wants, and a forced refresh's epoch), the task's envelope and
// Msg-Id, and the rule by which a clustarr-item-metadata record is
// incorporated into status.metadata.
//
// It is pure -- api types, pkg/events' names and schema only -- so the item
// packages that publish the task, the loop's metadata stage and the Artist
// and Author reconcilers that incorporate the answer share it without
// importing the gateway's package (app/catalog/metadata), which the manager
// must not link.
package metadatainputs
