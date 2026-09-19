//go:build e2e

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

package e2e

import (
	"context"
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestMediaFileTwoWriter is Phase H scenario 8 / amendment §A1.3: one
// MediaFile, two controllers, disjoint halves, proven from the apiserver's
// own managedFields bookkeeping rather than from which fields happen to be
// non-zero.
//
// The split is spec-versus-status, not status-versus-status: importarr
// creates the object and owns MediaFileSpec (what it observed on disk plus
// the release identity frozen at import, spec §8.4); catalogarr owns all of
// MediaFileStatus, plus metadata.labels, and only takes spec.sizeBytes/
// modTime/original over once it incorporates a transcode swap -- which
// cannot happen in Phase C, so catalogarr must claim no spec field here at
// all. The older "importarr on status.file/status.probe" wording in the
// remaining-work plan describes fields that do not exist.
//
// The second half is the part a naive test misses. Server-side apply
// REPLACES a manager's ownership set on every apply instead of merging it,
// so the interesting failure is a second apply releasing what the first
// wrote. That can only be observed on an object already in its steady state,
// so the probe refresh below runs against a MediaFile both managers have
// already written, not a fresh one.
func TestMediaFileTwoWriter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	rf := newRootFolder(ctx, t, "e2e-mf-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	filePath := path.Join(rf.Spec.Path, fixtureMovieFolder, fixtureMovieFile)
	plantMedia(t, hostPath(filePath))

	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	files := waitForMediaFileCount(ctx, t, rf.Spec.Path, 1)
	key := client.ObjectKeyFromObject(&files[0])
	movieName := files[0].Spec.MediaRef.Name
	cleanupUnlessFailed(t, func() {
		_ = k8sClient.Delete(context.Background(), &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: movieName, Namespace: Namespace},
		})
	})

	first := waitForBothWriters(ctx, t, key)
	firstProbedAt := first.Status.ProbedAt
	require.NotNil(t, firstProbedAt)

	// Force a genuine probe refresh. A second full scan on an untouched file
	// re-applies the identical spec, which is a no-op the apiserver does not
	// even bump the generation for; moving the mtime makes both legs real
	// work again -- importarr re-applies spec.modTime, and
	// mediainfo.ProbeHash changes, so catalogarr re-probes and re-applies
	// status.
	refreshAt := time.Now().Add(-time.Minute)
	require.NoError(t, os.Chtimes(hostPath(filePath), refreshAt, refreshAt))

	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	// status.probedAt is a metav1.Time, which serialises at RFC 3339 SECOND
	// granularity, so this is an ordering assumption on a truncated clock: two
	// probes inside the same wall-clock second are indistinguishable here and
	// would read as "not re-probed yet". It is safe because a scan cycle --
	// create the LibraryScan, dispatch through JetStream, walk, checkpoint,
	// roll up, re-probe -- comfortably exceeds a second, and the wait simply
	// polls until the next second ticks over if it ever did not. Anything that
	// makes the round trip sub-second must switch to comparing status.probeHash
	// instead, which changes with the mtime rather than with the clock.
	waitFor(t, ctx, 5*time.Minute, "MediaFile "+key.Name+" re-probed after the mtime change", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.MediaFile
		if err := k8sClient.Get(ctx, key, &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return live.Status.ProbedAt != nil && live.Status.ProbedAt.After(firstProbedAt.Time), nil
	}, describeMediaFile(key))

	second := waitForBothWriters(ctx, t, key)
	require.Equal(t, first.UID, second.UID,
		"the probe refresh must update the existing MediaFile, not create a second one")
	require.Len(t, mediaFilesUnder(ctx, t, rf.Spec.Path), 1,
		"a re-scan of the same file must not fork a second MediaFile")

	// Neither manager lost what it owns across the other's apply. These are
	// the fields the three observed Phase C release bugs would have zeroed.
	require.Equal(t, filePath, second.Spec.Path, "importarr's spec.path survived catalogarr's applies")
	require.NotZero(t, second.Spec.SizeBytes)
	require.NotEmpty(t, second.Spec.Quality.Name)
	require.NotNil(t, second.Status.MediaInfo, "catalogarr's status.mediaInfo survived importarr's applies")
	require.NotEmpty(t, second.Status.ProbeHash)
	require.NotEmpty(t, second.Status.Conditions)
}

// waitForBothWriters polls one MediaFile until both field managers have
// applied, then asserts the ownership split from managedFields. Every
// assertion is about who CLAIMS a field, not about its value.
func waitForBothWriters(ctx context.Context, t *testing.T, key client.ObjectKey) catalogv1alpha1.MediaFile {
	t.Helper()
	var mf catalogv1alpha1.MediaFile
	waitFor(t, ctx, 5*time.Minute, "MediaFile "+key.Name+" written by both importarr and catalogarr", func(ctx context.Context) (bool, error) {
		if err := k8sClient.Get(ctx, key, &mf); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		importarrOK, catalogarrOK := false, false
		for _, e := range mf.GetManagedFields() {
			switch e.Manager {
			case rescan.FieldManager.String():
				if ok, err := managedFieldsTouch(e, "spec.path", "spec.sizeBytes", "spec.mediaRef"); err == nil && ok {
					importarrOK = true
				}
			case k8s.ManagerCatalogarr.String():
				if ok, err := managedFieldsTouch(e, "status.mediaInfo", "status.probeHash", "status.conditions"); err == nil && ok {
					catalogarrOK = true
				}
			}
		}
		return importarrOK && catalogarrOK, nil
	}, describeMediaFile(key))

	var (
		importarrSpec  bool
		catalogarrStat bool
	)
	for _, e := range mf.GetManagedFields() {
		switch e.Manager {
		case rescan.FieldManager.String():
			ok, err := managedFieldsTouch(e, "spec.path")
			require.NoError(t, err)
			importarrSpec = importarrSpec || ok

			// importarr never writes MediaFileStatus, and never the labels
			// catalogarr mirrors onto the object.
			claims, err := managedFieldsTouch(e, "status")
			require.NoError(t, err)
			require.False(t, claims, "importarr's %q managedFields entry must not claim any status field", e.Subresource)
			claims, err = managedFieldsTouch(e, "metadata.labels")
			require.NoError(t, err)
			require.False(t, claims, "importarr must not claim metadata.labels, which catalogarr mirrors")

		case k8s.ManagerCatalogarr.String():
			if e.Subresource == "status" {
				ok, err := managedFieldsTouch(e, "status.mediaInfo", "status.probeHash", "status.probedAt", "status.conditions")
				require.NoError(t, err)
				catalogarrStat = catalogarrStat || ok
			}
			// Phase C has no transcode, so catalogarr has not taken
			// spec.sizeBytes/modTime/original over from importarr and must
			// claim no spec field at all.
			claims, err := managedFieldsTouch(e, "spec")
			require.NoError(t, err)
			require.False(t, claims,
				"catalogarr's %q managedFields entry claims a spec field; only an incorporated transcode swap may do that", e.Subresource)
		}
	}
	require.True(t, importarrSpec, "no importarr managedFields entry claims spec.path")
	require.True(t, catalogarrStat, "no catalogarr managedFields entry on the status subresource claims the probe result")
	return mf
}

// describeMediaFile renders one MediaFile's spec, probe state and, above
// all, its managedFields owners -- the thing scenario 8 is about, and the
// thing a bare "context deadline exceeded" hides completely.
func describeMediaFile(key client.ObjectKey) func() string {
	return func() string {
		var live catalogv1alpha1.MediaFile
		if err := k8sClient.Get(context.Background(), key, &live); err != nil {
			return fmt.Sprintf("MediaFile %s could not be read back: %v", key.Name, err)
		}
		out := fmt.Sprintf("MediaFile %s path=%q sizeBytes=%d probeHash=%q mediaInfo=%t",
			key.Name, live.Spec.Path, live.Spec.SizeBytes, live.Status.ProbeHash, live.Status.MediaInfo != nil)
		for _, e := range live.GetManagedFields() {
			fields := ""
			if e.FieldsV1 != nil {
				fields = string(e.FieldsV1.GetRawBytes())
			}
			out += fmt.Sprintf("\n    manager=%s subresource=%q fields=%s", e.Manager, e.Subresource, fields)
		}
		for _, c := range live.Status.Conditions {
			out += fmt.Sprintf("\n    condition %s=%s reason=%s message=%q", c.Type, c.Status, c.Reason, c.Message)
		}
		return out
	}
}
