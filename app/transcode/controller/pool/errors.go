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

package pool

import (
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// ErrNoAppliedSpec is [Render] refusing a stored pool Job that has lost its
// applied-template annotation (an operator's edit): a Job that is not
// [Mutable] must be re-sent the template it was applied with, and there is
// none to send. The reconciler recreates such a pool (ruling R15).
var ErrNoAppliedSpec = errors.New("no applied spec to keep")

// IsSchedulingImmutable reports the apiserver refusing to add
// .spec.scheduling to a Job created before WorkloadWithJob was enabled
// ("field cannot be set once created", spec §7): that pool is recreated.
func IsSchedulingImmutable(err error) bool {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return apierrors.IsInvalid(err) && strings.Contains(msg, "spec.scheduling") &&
		(strings.Contains(msg, "cannot be set once created") || strings.Contains(msg, "field is immutable"))
}

// IsPodFailurePolicyImmutable reports the apiserver refusing an apply that
// changes .spec.podFailurePolicy, which a Job never lets change: a pool
// created before the policy did (final-review I1 added the exit-137 rule)
// takes no apply again until it is recreated.
func IsPodFailurePolicyImmutable(err error) bool {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return apierrors.IsInvalid(err) && strings.Contains(msg, "spec.podFailurePolicy") &&
		strings.Contains(msg, "field is immutable")
}

// IsRecreateOnly reports an apply refused because the stored pool Job was
// created with something the apiserver never lets change and every apply
// sends: no gang minCount ([IsSchedulingImmutable]) or an older pod failure
// policy ([IsPodFailurePolicyImmutable]). Such a pool is drained and
// recreated.
func IsRecreateOnly(err error) bool {
	return IsSchedulingImmutable(err) || IsPodFailurePolicyImmutable(err)
}
