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

package transcodejob

import (
	"context"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// HandleEventForTest is the results consumer's handler, for a test that
// delivers worker status events without a subscription.
var HandleEventForTest = (*Reconciler).handleEvent

// AdmissionRequestForTest is the request that runs one admission pass, as
// the results consumer's wake and the pool Job watch enqueue it.
var AdmissionRequestForTest = admissionRequest

// PatchCASForTest is the one status write, for a test that races it.
func (r *Reconciler) PatchCASForTest(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) error {
	return r.patchCAS(ctx, tj, st)
}
