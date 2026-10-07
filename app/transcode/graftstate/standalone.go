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

package graftstate

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

// StandaloneFor reports whether a standalone graft is rewriting file now:
// Grafting's answer for one file, through the AudioGraft index the caller
// names (one keyed by status.mediaFileRef), so the transcode planner reads
// one file's grafts per pass, not every AudioGraft. Retired with AudioGraft
// (F7.3).
func StandaloneFor(ctx context.Context, c client.Reader, index string, file types.NamespacedName) (bool, error) {
	var l transcodev1alpha1.AudioGraftList
	if err := c.List(ctx, &l, client.InNamespace(file.Namespace), client.MatchingFields{index: file.Name}); err != nil {
		return false, fmt.Errorf("graftstate: list AudioGrafts of %s: %w", file, err)
	}
	for i := range l.Items {
		if Standalone(&l.Items[i]) {
			return true, nil
		}
	}
	return false, nil
}
