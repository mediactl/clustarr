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

// Package mfindex names the MediaFile indexes the remediation loop registers
// (loop spec §3.16). It imports nothing of app/remediation, so a domain
// package the loop calls can read an index by name without an import cycle.
package mfindex

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// UID indexes a MediaFile by metadata.uid: the task sweep, a records waker's
// ref with no name, and segmentplan.Sweeper.
const UID = "remediation.mediafile.uid"

// Register registers the loop's MediaFile indexes on idx. F4.1 adds Item.
func Register(ctx context.Context, idx client.FieldIndexer) error {
	return idx.IndexField(ctx, &catalogv1alpha1.MediaFile{}, UID, func(o client.Object) []string {
		if uid := o.GetUID(); uid != "" {
			return []string{string(uid)}
		}
		return nil
	})
}
