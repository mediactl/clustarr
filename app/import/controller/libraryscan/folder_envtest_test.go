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

package libraryscan_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/controller/libraryscan"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A folder the walk could not identify (the non-video design's D16) keeps
// its folder flag through every path that writes status.unmatched: the poll
// that first reports it (unmatchedACs), the poll that carries it once the
// worker no longer reports the path (aggregate's copy of the status entry),
// and an apply re-asserted from the object once the checkpoint is gone (the
// dead-letter fold, through baseStatus). The Unmatched page draws its "Add
// as artist" row from the flag, and import is the field's only owner, so a
// path that drops it clears it.
func TestReconcileKeepsAnUnidentifiedFolderMarked(t *testing.T) {
	ctx := context.Background()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, "ls-folder")
	started := metav1.NewTime(time.Now())
	scan := seedScan(t, ctx, c, ns, catalogv1alpha1.LibraryScanSpec{RootFolderRef: "music"},
		catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started})
	bus := newBus(t, ctx)
	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}

	candidates := []string{
		"Nirvana (90s US grunge band) [5b11f4ce-a62d-471e-81fc-a69a8278c7da]",
		"Nirvana (60s band from the UK) [9282c8b4-ca0b-4c6b-b7e3-4f7762dfc4d6]",
	}
	putProgress(t, ctx, bus, string(scan.UID), scanprogress.Progress{
		FilesSeen: 14,
		Unmatched: []scanprogress.UnmatchedFile{
			{Path: "Nirvana", Reason: "two artists are named Nirvana", Candidates: candidates, Folder: true, SeenAt: started.Time},
			{Path: "Loose/track.flac", Reason: "no match", SeenAt: started.Time},
		},
	})
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	assertFolderEntry(t, getScan(t, ctx, c, ns, scan.Name), candidates, "the poll that reports it")

	// The redelivery, restarted from the top, has not reached the folder yet:
	// the entry is carried from status.
	putProgress(t, ctx, bus, string(scan.UID), scanprogress.Progress{
		FilesSeen: 2,
		Unmatched: []scanprogress.UnmatchedFile{{Path: "Other/x.flac", Reason: "no match", SeenAt: started.Add(time.Minute)}},
	})
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	carried := getScan(t, ctx, c, ns, scan.Name)
	assertFolderEntry(t, carried, candidates, "the poll that carries it from status")
	assert.Equal(t, []string{string(k8s.ManagerImport)}, managersFor(t, carried.ManagedFields, "status", "status.unmatched"),
		"import alone owns status.unmatched, so a dropped flag is not held by a co-owner")

	// The checkpoint is gone and the scan dead-letters: the failure is
	// re-asserted from the object.
	require.NoError(t, bus.KV(events.BucketProgress).Delete(ctx, scanprogress.ProgressKey(string(scan.UID))))
	var cur catalogv1alpha1.LibraryScan
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(scan), &cur))
	patch := client.MergeFrom(cur.DeepCopy())
	cur.Annotations = map[string]string{k8s.AnnotationDeadLettered: "clustarr.work.import.scan.music@2026-11-10T10:00:00Z"}
	require.NoError(t, c.Patch(ctx, &cur, patch))
	require.NoError(t, errOf(r.Reconcile(ctx, request(ns, scan.Name))))
	failed := getScan(t, ctx, c, ns, scan.Name)
	require.Equal(t, catalogv1alpha1.ScanPhaseFailed, failed.Status.Phase)
	assertFolderEntry(t, failed, candidates, "the failure re-asserted from the object")
}

// assertFolderEntry finds the "Nirvana" entry and checks it is still the
// folder entry the walk reported, and that the file entry beside it never
// became a folder.
func assertFolderEntry(t *testing.T, scan catalogv1alpha1.LibraryScan, candidates []string, when string) {
	t.Helper()
	var folder, file *catalogv1alpha1.UnmatchedFile
	for i := range scan.Status.Unmatched {
		switch scan.Status.Unmatched[i].Path {
		case "Nirvana":
			folder = &scan.Status.Unmatched[i]
		case "Loose/track.flac":
			file = &scan.Status.Unmatched[i]
		}
	}
	require.NotNil(t, folder, "%s: the folder entry is listed", when)
	assert.True(t, folder.Folder, "%s: the entry is marked a folder", when)
	assert.Equal(t, candidates, folder.Candidates, "%s: the hits are its candidates", when)
	require.NotNil(t, file, "%s: the file entry is listed", when)
	assert.False(t, file.Folder, "%s: a file entry is no folder", when)
}
