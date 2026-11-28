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

package manager

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// sinkRecord is what ownerEvents reported.
type sinkRecord struct {
	deleted []types.UID
	sweeps  int
}

func (s *sinkRecord) OwnerDeleted(_ schema.GroupVersionKind, _, _ string, uid types.UID) {
	s.deleted = append(s.deleted, uid)
}
func (s *sinkRecord) SweepSoon() { s.sweeps++ }

// An owner that went is reported whichever way its informer says so: a
// delete, a tombstone, or -- after a watch reconnects and relists -- an
// update from the object that went to the one made again under its name,
// which client-go delivers as an update and no delete. A delete that
// carries no object has no uid to collect for, so it asks for a sweep.
func TestOwnerEventsReportEveryWayAnOwnerGoes(t *testing.T) {
	series := func(uid string) *catalogv1.Series {
		return &catalogv1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "show", UID: types.UID(uid)}}
	}
	gvk := catalogv1.GroupVersion.WithKind("Series")
	cases := []struct {
		name    string
		fire    func(h toolscache.ResourceEventHandlerFuncs)
		deleted []types.UID
		sweeps  int
	}{
		{"a delete", func(h toolscache.ResourceEventHandlerFuncs) { h.OnDelete(series("a")) }, []types.UID{"a"}, 0},
		{"a tombstone with the object", func(h toolscache.ResourceEventHandlerFuncs) {
			h.OnDelete(toolscache.DeletedFinalStateUnknown{Key: "ns/show", Obj: series("a")})
		}, []types.UID{"a"}, 0},
		{"a tombstone without it", func(h toolscache.ResourceEventHandlerFuncs) {
			h.OnDelete(toolscache.DeletedFinalStateUnknown{Key: "ns/show"})
		}, nil, 1},
		{"a tombstone holding a nil object", func(h toolscache.ResourceEventHandlerFuncs) {
			h.OnDelete(toolscache.DeletedFinalStateUnknown{Key: "ns/show", Obj: (*catalogv1.Series)(nil)})
		}, nil, 1},
		{"an update to another uid", func(h toolscache.ResourceEventHandlerFuncs) { h.OnUpdate(series("a"), series("b")) }, []types.UID{"a"}, 0},
		{"an update of the same object", func(h toolscache.ResourceEventHandlerFuncs) { h.OnUpdate(series("a"), series("a")) }, nil, 0},
		{"an add", func(h toolscache.ResourceEventHandlerFuncs) { h.OnAdd(series("a"), false) }, nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got sinkRecord
			tc.fire(ownerEvents(&got, gvk))
			require.Equal(t, tc.deleted, got.deleted)
			require.Equal(t, tc.sweeps, got.sweeps)
		})
	}
}

// The collector reads the manager's cache for these kinds, so each must be
// one a controller sets as an owner and none may be a library kind: a cache
// read of a library kind would start a row informer for it (ADR-0025,
// part 7).
func TestLibraryOwnerKindsAreCustomResources(t *testing.T) {
	library := map[string]bool{"MediaFile": true, "Episode": true, "Album": true, "Book": true, "Audiobook": true, "Issue": true}
	for _, gvk := range libraryOwnerKinds {
		require.False(t, library[gvk.Kind], "%s is a library kind", gvk.Kind)
		require.Equal(t, catalogv1.GroupVersion, gvk.GroupVersion())
	}
}
