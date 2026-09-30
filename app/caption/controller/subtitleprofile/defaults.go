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

package subtitleprofile

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// DefaultProfileName is the name of the profile SeedDefault creates.
const DefaultProfileName = "english"

// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleprofiles,verbs=create

// SeedDefault creates, in namespace, the default SubtitleProfile "english"
// -- English wanted for every movie and episode no other profile selects
// whose audio is not already English (audioExclude, Bazarr's usual setup;
// untagged audio counts as the item's original language), every other
// field at the CRD's defaults -- when the cluster has no
// SubtitleProfile at all. Profiles are cluster-wide (spec.default is the
// cluster default), so any profile, in any namespace, means the owner has
// set subtitles up and nothing is created. It only ever creates; once the
// owner edits or replaces it, it is theirs. With namespace empty
// (`clustarr all` from a shell) it does nothing.
func SeedDefault(ctx context.Context, c client.Client, namespace string) error {
	log := logging.FromContext(ctx)
	if namespace == "" {
		log.Info("subtitleprofile: no namespace; default profile not seeded")
		return nil
	}
	var existing subtitlev1alpha1.SubtitleProfileList
	if err := c.List(ctx, &existing, client.Limit(1)); err != nil {
		return fmt.Errorf("subtitleprofile: list profiles: %w", err)
	}
	if len(existing.Items) > 0 {
		return nil
	}
	p := &subtitlev1alpha1.SubtitleProfile{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: DefaultProfileName},
		Spec: subtitlev1alpha1.SubtitleProfileSpec{
			Default:   true,
			Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en", AudioExclude: true}},
		},
	}
	if err := client.IgnoreAlreadyExists(c.Create(ctx, p)); err != nil {
		return fmt.Errorf("subtitleprofile: seed %s: %w", DefaultProfileName, err)
	}
	log.Info("subtitleprofile: seeded the default English profile", "namespace", namespace)
	return nil
}

// Bootstrap is a one-shot, leader-elected manager.Runnable that calls
// SeedDefault once at start.
type Bootstrap struct {
	Client    client.Client
	Namespace string
}

func (b *Bootstrap) Start(ctx context.Context) error {
	return SeedDefault(ctx, b.Client, b.Namespace)
}

func (b *Bootstrap) NeedLeaderElection() bool { return true }
