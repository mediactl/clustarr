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

package transcode

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/transcode/graftstate"
)

// Gather reads the profiles, whether the file's item exists and whether a
// standalone graft holds the file, from the manager cache.
func (a *Adapter) Gather(ctx context.Context, env *remediation.Env, v *remediation.View) (In, error) {
	in := In{Read: true}
	var profiles transcodev1alpha1.TranscodeProfileList
	if err := env.Reader.List(ctx, &profiles, client.UnsafeDisableDeepCopy); err != nil {
		return in, remediation.Transient(fmt.Errorf("transcode: list TranscodeProfiles: %w", err))
	}
	in.Profiles = profiles.Items
	exists, err := itemExists(ctx, env.Reader, v.File)
	if err != nil {
		return in, remediation.Transient(err)
	}
	in.ItemExists = exists
	if a.o.AudioGraftIndex != "" {
		if in.StandaloneGraft, err = graftstate.StandaloneFor(ctx, env.Reader, a.o.AudioGraftIndex, client.ObjectKeyFromObject(v.File)); err != nil {
			return in, remediation.Transient(err)
		}
	}
	return in, nil
}

// itemExists Gets the file's Movie or Episode from the cache: NotFound is
// false, any other error is returned.
func itemExists(ctx context.Context, r client.Reader, mf *catalogv1alpha1.MediaFile) (bool, error) {
	key := types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}
	var obj client.Object
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		obj = &catalogv1alpha1.Movie{}
	case commonv1.MediaKindEpisode:
		obj = &catalogv1alpha1.Episode{}
	default:
		return false, nil
	}
	switch err := r.Get(ctx, key, obj, client.UnsafeDisableDeepCopy); {
	case apierrors.IsNotFound(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("transcode: get %s %s: %w", mf.Spec.MediaRef.Kind, key, err)
	}
	return true, nil
}
