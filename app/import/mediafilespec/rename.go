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

package mediafilespec

import (
	"k8s.io/apimachinery/pkg/api/meta"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// The reasons a [RenameOutcome] carries. A move that happened has none.
const (
	// RenameNotCurrent: nothing to do. catalogarr proposes no path, or the
	// file is already at it.
	RenameNotCurrent = "NotCurrent"
	// RenameHeld: catalogarr holds the proposal (status.naming.reason is
	// set), or it names another folder -- this pass renames files only,
	// never folders (spec D5).
	RenameHeld = "Held"
	// RenameCollision: something already exists at the proposed path. It
	// is never overwritten.
	RenameCollision = "Collision"
	// RenameChanged: the file on disk is not what spec records -- its size
	// or mtime moved -- or the MediaFile changed while the rename wrote it.
	// Nothing is moved; the rescan re-observes the file.
	RenameChanged = "Changed"
	// RenameDryRun: every check passed and nothing was touched.
	RenameDryRun = "DryRun"
	// RenameFailed prefixes the error a LibraryScan's rename pass records
	// for a file RenameFile could not rename ("Failed: <error>"). It is
	// never a RenameOutcome's reason: RenameFile returns the error.
	RenameFailed = "Failed"
)

// Renameable reports whether mf is a rename candidate (ruling R20):
// catalogarr says its path is not the canonical one (NamingCurrent False),
// and the file is present and probed (Ready and Probed True). catalogarr
// holds a file it cannot name yet with NamingCurrent Unknown, so a held
// file never passes; RenameFile refuses one too. The rename controller and
// the LibraryScan rename pass both select with it.
func Renameable(mf *catalogv1alpha1.MediaFile) bool {
	c := mf.Status.Conditions
	return meta.IsStatusConditionFalse(c, catalogv1alpha1.ConditionNamingCurrent) &&
		meta.IsStatusConditionTrue(c, catalogv1alpha1.MediaFileConditionReady) &&
		meta.IsStatusConditionTrue(c, catalogv1alpha1.MediaFileConditionProbed)
}

// RenameOutcome is what [RenameFile] did with one MediaFile: From is its
// spec.path, To the path catalogarr proposes (empty when there is none),
// and either Moved or a Reason.
type RenameOutcome struct {
	From, To, Reason string
	Moved            bool
}
