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
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// UID indexes a MediaFile by metadata.uid: the task sweep, a records waker's
// ref with no name, and segmentplan.Sweeper.
const UID = "remediation.mediafile.uid"

// DownloadRef indexes a MediaFile by spec.importedFrom.downloadRef: the
// grab entry id that imported it (ADR-0019 §6.9). The downloads stage counts
// an entry's MediaFiles through it, which a Series' pack spreads over many
// Episodes' files.
const DownloadRef = "remediation.mediafile.downloadref"

// Register registers the loop's MediaFile indexes on idx: UID, Item and
// DownloadRef. A
// second registration on one cache is an "indexer conflict", so only
// remediation.RegisterIndexes calls it in a process, and tests that run a
// reader without the loop.
func Register(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &catalogv1alpha1.MediaFile{}, UID, func(o client.Object) []string {
		if uid := o.GetUID(); uid != "" {
			return []string{string(uid)}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("mfindex: register %s: %w", UID, err)
	}
	if err := idx.IndexField(ctx, Index.Object, Index.Name, Index.Extract); err != nil {
		return fmt.Errorf("mfindex: register %s: %w", Item, err)
	}
	if err := idx.IndexField(ctx, &catalogv1alpha1.MediaFile{}, DownloadRef, func(o client.Object) []string {
		mf, ok := o.(*catalogv1alpha1.MediaFile)
		if !ok || mf.Spec.ImportedFrom == nil || mf.Spec.ImportedFrom.DownloadRef == "" {
			return nil
		}
		return []string{mf.Spec.ImportedFrom.DownloadRef}
	}); err != nil {
		return fmt.Errorf("mfindex: register %s: %w", DownloadRef, err)
	}
	return nil
}
