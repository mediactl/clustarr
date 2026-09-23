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

package status_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr/status"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newDownload(t *testing.T, ctx context.Context, c client.Client, ns, name string) *downloadv1alpha1.Download {
	t.Helper()
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
			Release: commonv1.ReleaseInfo{
				GUID: "https://indexer.example/1", IndexerRef: "example", IndexerName: "Example",
				Title: "Arrival.2016.1080p", Protocol: commonv1.ProtocolTorrent,
				InfoHash: "0123456789abcdef0123456789abcdef01234567",
			},
			Target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "arrival"},
		},
	}
	require.NoError(t, c.Create(ctx, dl))
	return dl
}

// This is the test the package exists for.
//
// Download.status has three writers. Server-side apply replaces a field
// manager's ownership set on every apply, so if the controller, the engine and
// importarr did not have disjoint, completely-declared sets, each apply would
// release the others' fields -- and nothing would log it, because
// pkg/k8s.PatchStatus applies with ForceOwnership and the apiserver never
// reports a conflict.
//
// The object is driven to a populated steady state FIRST, by all three
// writers, before any of them applies again. A test that starts from a blank
// object cannot observe a release, because there is nothing to release.
//
// There is no co-owner to give a false pass here: the two grabarr declarations
// are proven disjoint by TestTheTwoDeclarationsAreDisjoint, and status.import
// is written once by a third manager that never applies again -- so if either
// grabarr manager claimed it, ForceOwnership would hand it over and the very
// next apply would release it.
func TestTheTwoManagersDoNotReleaseEachOthersFields(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	const ns = "grabarr-status-split"
	require.NoError(t, client.IgnoreAlreadyExists(
		c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	dl := newDownload(t, ctx, c, ns, "split")
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	lastProgress := now.Time

	// Steady state: the controller's nine fields.
	require.NoError(t, status.Patch(ctx, c, k8s.ManagerGrabarr, dl,
		func(ac *downloadac.DownloadStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).
				WithPhase(downloadv1alpha1.DownloadPhaseSeeding).
				WithEngine("torrents-0").
				WithFailureReason(downloadv1alpha1.DownloadFailureNone).
				WithBlocklistedUntil(now).
				WithStartedAt(now).
				WithCompletedAt(now).
				WithSeedGoalMetAt(now).
				WithConditions(k8s.ConditionAC(metav1.Condition{
					Type: downloadv1alpha1.DownloadConditionAssigned, Status: metav1.ConditionTrue,
					Reason: "Assigned", LastTransitionTime: now, ObservedGeneration: 1,
				}))
		}))

	// Steady state: the engine's twenty-six, through the same mapping the
	// engines themselves write with.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dl), dl))
	require.NoError(t, status.Patch(ctx, c, k8s.ManagerGrabarrEngine, dl,
		func(ac *downloadac.DownloadStatusApplyConfiguration) {
			*ac = *download.ApplyStatus(download.Item{
				ID:              "0123456789abcdef0123456789abcdef01234567",
				Stage:           downloadv1alpha1.DownloadStageSeeding,
				TotalBytes:      8 << 30,
				RemainingBytes:  0,
				DownloadedBytes: 8 << 30,
				UploadedBytes:   12 << 30,
				DownRate:        0,
				UpRate:          3_000_000,
				ProgressPercent: 100,
				RatioMilli:      1500,
				SeedTime:        2 * time.Hour,
				Seeders:         42,
				Peers:           17,
				Health:          &downloadv1alpha1.UsenetHealth{HealthPercent: 99, CriticalHealthPercent: 91, FailedArticles: 3, TotalArticles: 4096},
				OutputPath:      "/data/torrents/movies/split",
				ContentRoot:     "/data/torrents/movies/split",
				Files:           []download.File{{Path: "split.mkv", SizeBytes: 8 << 30}, {Path: "sample.mkv", SizeBytes: 1 << 20, Skipped: true}},
				CanMoveFiles:    true,
				CanBeRemoved:    true,
				IsEncrypted:     true,
				Message:         "seeding",
				LastProgressAt:  &lastProgress,
				SeedGoalMet:     true,
				HealthPaused:    true,
				// Not a coherent transfer -- a seeding torrent that also
				// failed -- but this test is about ownership of every leaf,
				// and engineFailureReason is only sent while it is set.
				Status:        download.StatusFailed,
				FailureReason: downloadv1alpha1.DownloadFailureWriteError,
			})
		}))

	// Steady state: the third writer, importarr. It applies exactly once and
	// never again, so any claim on status.import by either grabarr manager
	// shows up as a deletion rather than being masked by a co-owner that
	// keeps writing the same value back.
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerImportarr,
		downloadac.Download(dl.Name, ns).WithStatus(downloadac.DownloadStatus().
			WithImport(downloadac.ImportState().
				WithState(downloadv1alpha1.ImportPhaseImported).
				WithImportedAt(now))))
	require.NoError(t, err)

	var seeded downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dl), &seeded))
	require.Equal(t, downloadv1alpha1.DownloadPhaseSeeding, seeded.Status.Phase, "setup: the controller half did not land")
	require.EqualValues(t, 1500, seeded.Status.RatioMilli, "setup: the engine half did not land")
	require.NotNil(t, seeded.Status.Import, "setup: importarr's half did not land")

	// Now each manager applies again, changing only one of its own fields.
	require.NoError(t, status.Patch(ctx, c, k8s.ManagerGrabarr, &seeded,
		func(ac *downloadac.DownloadStatusApplyConfiguration) {
			ac.WithObservedGeneration(2).WithConditions(k8s.ConditionAC(metav1.Condition{
				Type: downloadv1alpha1.DownloadConditionAssigned, Status: metav1.ConditionTrue,
				Reason: "StillAssigned", LastTransitionTime: now, ObservedGeneration: 2,
			}))
		}))

	var afterController downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dl), &afterController))
	assertEngineHalfIntact(t, afterController.Status, "the controller apply")
	assertImportIntact(t, afterController.Status, "the controller apply")

	require.NoError(t, status.Patch(ctx, c, k8s.ManagerGrabarrEngine, &afterController,
		func(ac *downloadac.DownloadStatusApplyConfiguration) { ac.WithMessage("still seeding") }))

	var afterEngine downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dl), &afterEngine))
	assertControllerHalfIntact(t, afterEngine.Status, "the engine apply")
	assertImportIntact(t, afterEngine.Status, "the engine apply")
	assert.Equal(t, "still seeding", afterEngine.Status.Message, "the engine's own field did not update")

	// And the half a manager owns must survive ITS OWN re-apply. This is the
	// assertion D1's first version of this test lacked: it checked only that
	// each manager left the OTHER's half alone, so a declaration that forgot
	// one of its own fields passed cleanly while deleting it on every write.
	assertEngineHalfIntact(t, afterEngine.Status, "the engine's own re-apply")
	assertControllerHalfIntact(t, afterController.Status, "the controller's own re-apply")
	assert.EqualValues(t, 2, afterController.Status.ObservedGeneration, "the controller's own field did not update")

	// status.health is a struct, and server-side apply tracks ownership per
	// LEAF inside it rather than for the sub-object as a whole. A renderer
	// that sent only healthPercent would pass a NotNil check while silently
	// releasing the other three on every write.
	if h := afterEngine.Status.Health; assert.NotNil(t, h, "the engine apply released status.health") {
		assert.EqualValues(t, 99, h.HealthPercent, "released health.healthPercent")
		assert.EqualValues(t, 91, h.CriticalHealthPercent, "released health.criticalHealthPercent")
		assert.EqualValues(t, 3, h.FailedArticles, "released health.failedArticles")
		assert.EqualValues(t, 4096, h.TotalArticles, "released health.totalArticles")
	}

	// status.files is a listType=map keyed on path: ownership is tracked per
	// ENTRY, so a declaration that dropped one entry would remove exactly that
	// file and leave the rest looking healthy.
	if assert.Len(t, afterEngine.Status.Files, 2, "the engine apply released a status.files entry") {
		byPath := map[string]downloadv1alpha1.DownloadFile{}
		for _, f := range afterEngine.Status.Files {
			byPath[f.Path] = f
		}
		assert.EqualValues(t, 8<<30, byPath["split.mkv"].SizeBytes, "released files[split.mkv].sizeBytes")
		assert.True(t, byPath["sample.mkv"].Skipped, "released files[sample.mkv].skipped")
	}

	// Conditions is a listType=map too, and ControllerFields leaves it unseeded
	// so the caller sets it exactly once. Re-applying with a changed reason
	// must update the entry rather than duplicate or drop it.
	if assert.Len(t, afterEngine.Status.Conditions, 1, "the conditions list did not survive") {
		assert.Equal(t, "StillAssigned", afterEngine.Status.Conditions[0].Reason)
	}

	// Finally, assert OWNERSHIP rather than values.
	//
	// Every assertion above compares what is on the object, and that class of
	// assertion structurally cannot see an over-claim: if a grabarr manager
	// started declaring status.import, server-side apply would hand the leaf
	// over under ForceOwnership, the value would be identical, importarr would
	// keep owning the leaves grabarr did not name, and nothing on the object
	// would change. It was verified that this is not hypothetical -- adding an
	// import claim to ControllerFields leaves every value assertion above
	// passing. That is the co-owner false pass, and managedFields is the only
	// place it is visible.
	assertManagedFieldsSplit(t, &afterEngine)
}

// assertManagedFieldsSplit reads the apiserver's own record of who owns what
// and holds it to the declared split, leaf for leaf.
func assertManagedFieldsSplit(t *testing.T, dl *downloadv1alpha1.Download) {
	t.Helper()

	// etaSeconds is absent from the engine's half here because the Item had no
	// estimate; see assertEngineHalfIntact.
	want := map[string][]string{
		string(k8s.ManagerGrabarr):       controllerOwned,
		string(k8s.ManagerGrabarrEngine): without(engineOwned, "ETASeconds"),
		string(k8s.ManagerImportarr):     foreignOwned,
	}

	seen := map[string]bool{}
	for _, entry := range dl.ManagedFields {
		if entry.Subresource != "status" || entry.FieldsV1 == nil {
			continue
		}
		expected, ok := want[entry.Manager]
		require.Truef(t, ok, "%q owns fields on Download.status but is no part of the split", entry.Manager)
		seen[entry.Manager] = true

		var fields map[string]any
		require.NoError(t, json.Unmarshal(entry.FieldsV1.GetRawBytes(), &fields))
		status, ok := fields["f:status"].(map[string]any)
		require.Truef(t, ok, "%q has a status managedFields entry with no f:status", entry.Manager)

		var got []string
		for key := range status {
			got = append(got, strings.TrimPrefix(key, "f:"))
		}
		sort.Strings(got)
		assert.Equalf(t, jsonNames(expected), got,
			"the apiserver records %q as owning a different set than it declares", entry.Manager)
	}
	for manager := range want {
		assert.Truef(t, seen[manager], "%q owns nothing on Download.status; its apply never landed", manager)
	}
}

// jsonNames maps Go field names onto the JSON names managedFields uses.
func jsonNames(goNames []string) []string {
	typ := reflect.TypeOf(downloadv1alpha1.DownloadStatus{})
	out := make([]string, 0, len(goNames))
	for _, name := range goNames {
		f, ok := typ.FieldByName(name)
		if !ok {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

func without(names []string, drop string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

func assertControllerHalfIntact(t *testing.T, st downloadv1alpha1.DownloadStatus, who string) {
	t.Helper()
	assert.EqualValues(t, 2, st.ObservedGeneration, who+" released observedGeneration")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseSeeding, st.Phase, who+" released phase")
	assert.Equal(t, "torrents-0", st.Engine, who+" released engine")
	assert.Equal(t, downloadv1alpha1.DownloadFailureNone, st.FailureReason, who+" released failureReason")
	assert.NotNil(t, st.BlocklistedUntil, who+" released blocklistedUntil")
	assert.NotNil(t, st.StartedAt, who+" released startedAt")
	assert.NotNil(t, st.CompletedAt, who+" released completedAt")
	assert.NotNil(t, st.SeedGoalMetAt, who+" released seedGoalMetAt")
	assert.NotEmpty(t, st.Conditions, who+" released conditions")
}

func assertEngineHalfIntact(t *testing.T, st downloadv1alpha1.DownloadStatus, who string) {
	t.Helper()
	assert.Equal(t, downloadv1alpha1.DownloadStageSeeding, st.Stage, who+" released stage")
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", st.DownloadID, who+" released downloadID")
	assert.Equal(t, "/data/torrents/movies/split", st.OutputPath, who+" released outputPath")
	assert.Equal(t, "/data/torrents/movies/split", st.ContentRoot, who+" released contentRoot")
	assert.Len(t, st.Files, 2, who+" released files")
	assert.EqualValues(t, 8<<30, st.TotalBytes, who+" released totalBytes")
	assert.EqualValues(t, 0, st.RemainingBytes, who+" released remainingBytes")
	assert.EqualValues(t, 8<<30, st.DownloadedBytes, who+" released downloadedBytes")
	assert.EqualValues(t, 12<<30, st.UploadedBytes, who+" released uploadedBytes")
	assert.EqualValues(t, 0, st.DownloadRateBps, who+" released downloadRateBps")
	assert.EqualValues(t, 3_000_000, st.UploadRateBps, who+" released uploadRateBps")
	assert.EqualValues(t, 100, st.ProgressPercent, who+" released progressPercent")
	assert.EqualValues(t, 42, st.Seeders, who+" released seeders")
	assert.EqualValues(t, 17, st.Peers, who+" released peers")
	assert.EqualValues(t, 1500, st.RatioMilli, who+" released ratioMilli")
	assert.EqualValues(t, 7200, st.SeedTimeSeconds, who+" released seedTimeSeconds")
	assert.NotNil(t, st.Health, who+" released health")
	assert.True(t, st.IsEncrypted, who+" released isEncrypted")
	assert.True(t, st.CanMoveFiles, who+" released canMoveFiles")
	assert.True(t, st.CanBeRemoved, who+" released canBeRemoved")
	assert.NotEmpty(t, st.Message, who+" released message")
	assert.NotNil(t, st.LastProgressAt, who+" released lastProgressAt")
	assert.Equal(t, downloadv1alpha1.DownloadFailureWriteError, st.EngineFailureReason, who+" released engineFailureReason")
	assert.True(t, st.SeedGoalReached, who+" released seedGoalReached")
	assert.True(t, st.HealthPaused, who+" released healthPaused")
	// etaSeconds is deliberately absent: the Item above has no estimate, and
	// "absent" is the value the CRD documents for that. It is listed here so
	// that the twenty-six-field set reads as complete rather than as
	// twenty-five with one forgotten.
	assert.Nil(t, st.ETASeconds, who+" invented an etaSeconds")
}

func assertImportIntact(t *testing.T, st downloadv1alpha1.DownloadStatus, who string) {
	t.Helper()
	if assert.NotNil(t, st.Import, who+" released status.import, which belongs to importarr") {
		assert.Equal(t, downloadv1alpha1.ImportPhaseImported, st.Import.State)
		assert.NotNil(t, st.Import.ImportedAt, who+" released status.import.importedAt")
	}
}

// A manager outside the split must be refused rather than allowed to claim
// fields that neither half accounts for. k8s.ManagerImportarr is refused too:
// it legitimately writes status.import, but that write is importarr's own and
// routing it through this declaration would hand it grabarr's owned set as
// well.
func TestPatchRefusesAManagerOutsideTheSplit(t *testing.T) {
	for _, mgr := range []k8s.FieldManager{k8s.ManagerCatalogarr, k8s.ManagerImportarr, k8s.FieldManager("nonsense")} {
		err := status.Patch(context.Background(), nil, mgr,
			&downloadv1alpha1.Download{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "y"}}, nil)
		require.ErrorContains(t, err, "owns no part of Download.status")
	}
}
