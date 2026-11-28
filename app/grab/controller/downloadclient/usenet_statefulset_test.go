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

package downloadclient

import (
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// buildUsenetStatefulSet is the usenet engine's StatefulSet as the
// controller renders it for a client with a fixed engine count.
func buildUsenetStatefulSet(dc *downloadv1alpha1.DownloadClient, name, image, dataDir, scratchDir, dataClaimName string, rt EngineRuntime, secrets map[string]string, owner *metav1ac.OwnerReferenceApplyConfiguration) *appsv1ac.StatefulSetApplyConfiguration {
	return buildEngineStatefulSet(dc, fixedReplicas(dc), name, image, dataDir, scratchDir, dataClaimName, rt, secrets, owner)
}
