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

package k8s

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// AddToSchemeFuncs is every group a Clustarr manager has to understand.
//
// All five services share one scheme even though each only writes its own
// group: §10 has every service watching at least one other group read-only,
// and the built-in groups are what the managers create and consume --
// batch/v1 Jobs for squasharr, apps/v1 StatefulSets and Deployments for
// grabarr engines, core/v1 Secrets, ConfigMaps and PVCs everywhere,
// coordination/v1 Leases for leader election and events.k8s.io/v1 for the
// history sink in §13.
var AddToSchemeFuncs = []func(*runtime.Scheme) error{
	// Kubernetes built-ins.
	corev1.AddToScheme,
	batchv1.AddToScheme,
	appsv1.AddToScheme,
	coordinationv1.AddToScheme,
	eventsv1.AddToScheme,

	// Clustarr API groups. api/common/v1alpha1 holds shared Go types only
	// and has no GroupVersion of its own, so there is nothing to register.
	catalogv1alpha1.AddToScheme,
	indexv1alpha1.AddToScheme,
	downloadv1alpha1.AddToScheme,
	transcodev1alpha1.AddToScheme,
	subtitlev1alpha1.AddToScheme,
}

// AddToScheme registers every group from [AddToSchemeFuncs] into s.
func AddToScheme(s *runtime.Scheme) error {
	if s == nil {
		return fmt.Errorf("k8s: nil scheme")
	}
	for _, add := range AddToSchemeFuncs {
		if err := add(s); err != nil {
			return fmt.Errorf("k8s: build scheme: %w", err)
		}
	}
	// metav1 gives the scheme ListOptions and friends, which the
	// PartialObjectMetadata and metadata-only cache paths need.
	metav1.AddToGroupVersion(s, corev1.SchemeGroupVersion)
	return nil
}

// NewScheme returns a scheme with every group a Clustarr manager needs.
func NewScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// MustNewScheme is [NewScheme] for package-level initialisation. The only way
// it can fail is a programming error in a generated AddToScheme, which is not
// worth threading an error through every service's main.
func MustNewScheme() *runtime.Scheme {
	s, err := NewScheme()
	utilruntime.Must(err)
	return s
}
