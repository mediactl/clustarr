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

package grab

import (
	"context"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// PerformGrabForTest exposes performGrab to the black-box grab_test package.
// performGrab stays unexported because Decide and Handler.Handle are the only
// entry points production code should have: a caller that skips Decide skips
// the delay profile entirely.
func PerformGrabForTest(
	ctx context.Context,
	d Deps,
	ns string,
	target commonv1.MediaRef,
	keys []string,
	release commonv1.ReleaseInfo,
	grabbedBy downloadv1alpha1.GrabSource,
) error {
	return performGrab(ctx, d, ns, target, keys, release, grabbedBy)
}
