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

package artist

import (
	corev1 "k8s.io/api/core/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// normal and warn record a Kubernetes Event on a under the "artist"
// recorder (2026-11-10 design D14): a phase edge, a fan-out that created
// albums, a folder scan asked for, a condition edge. A nil Recorder records
// nothing.
func (r *Reconciler) normal(a *catalogv1alpha1.Artist, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(a, nil, corev1.EventTypeNormal, reason, "Reconcile", format, args...)
	}
}

func (r *Reconciler) warn(a *catalogv1alpha1.Artist, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(a, nil, corev1.EventTypeWarning, reason, "Reconcile", format, args...)
	}
}
