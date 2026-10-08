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

package itemtest

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// fixtureInfoHash is the info hash every Entry carries: a valid 40-hex
// value, so the release's pattern admits it.
const fixtureInfoHash = "0123456789abcdef0123456789abcdef01234567"

// Entry is a minimal admissible grab entry for an item's status.downloads
// (ADR-0019 §6.11): a torrent by magnet, grabbed by an automatic search at
// the current second, in phase, its dispatch at seq 1 -- the fields
// grabplan writes, less the release's decision fields no rollup reads.
// The caller sets Episodes, Issues or Purpose where the owner needs them.
func Entry(id string, phase commonv1.DownloadPhase) catalogv1alpha1.DownloadEntry {
	magnet := "magnet:?xt=urn:btih:" + fixtureInfoHash
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	return catalogv1alpha1.DownloadEntry{
		ID:  id,
		UID: string(uuid.NewUUID()),
		Release: catalogv1alpha1.DownloadRelease{
			Title: "Fixture.2016.1080p", GUID: "https://indexer.example/" + id,
			IndexerRef: "example", IndexerName: "Example",
			InfoHash: fixtureInfoHash, Protocol: commonv1.ProtocolTorrent,
		},
		Source:             commonv1.DownloadSource{MagnetURL: &magnet},
		Client:             "torrent",
		GrabbedBy:          commonv1.GrabSourceSearch,
		GrabbedAt:          now,
		RemoveOnImport:     true,
		RemoveDataOnDelete: true,
		Phase:              phase,
		Dispatch:           catalogv1alpha1.Dispatch{Seq: 1, DispatchedAt: now},
	}
}

// SeedEntries applies entries as obj's whole status.downloads under
// k8s.ManagerGrab -- a test's stand-in for the grab and downloads stages,
// whose entries ride the item's own catalog apply in production
// (itempass). An item reconciler re-sends the entries it reads, so its next
// apply co-owns them. No item watch wakes on this write (the stages decide
// entries in the pass that applies them), so the caller waits for its
// cache to see the entries and then reconciles the item itself. No entries
// clears the list.
func SeedEntries(ctx context.Context, c client.Client, obj client.Object, entries ...catalogv1alpha1.DownloadEntry) error {
	gvk, err := apiutil.GVKForObject(obj, c.Scheme())
	if err != nil {
		return err
	}
	list := make([]any, 0, len(entries))
	for i := range entries {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&entries[i])
		if err != nil {
			return fmt.Errorf("itemtest: entry %s: %w", entries[i].ID, err)
		}
		list = append(list, m)
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name":      obj.GetName(),
			"namespace": obj.GetNamespace(),
		},
		"status": map[string]any{"downloads": list},
	}}
	_, err = k8s.PatchStatusUnstructured(ctx, c, k8s.ManagerGrab, u)
	return err
}
