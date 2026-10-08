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

package probe_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// The old package's fixtures, under their old names.
var (
	mustNamespace        = remediationtest.MustNamespace
	mustQualityProfile   = remediationtest.MustQualityProfile
	mustMovie            = remediationtest.MustMovie
	writeFile            = remediationtest.WriteFile
	fakeProbe            = remediationtest.FakeProbe
	fakeProber           = remediationtest.FakeProber
	fakeMediaInfo        = remediationtest.FakeMediaInfo
	fieldManagerNames    = remediationtest.FieldManagerNames
	managedFieldPaths    = remediationtest.ManagedFieldPaths
	reconcileOnce        = remediationtest.ReconcileOnce
	newProbeHarness      = remediationtest.NewProbeHarness
	webdl720             = remediationtest.WebDL720
	bluray1080p          = remediationtest.Bluray1080p
	catalogManager       = k8s.ManagerCatalog.String()
	importManager        = mediafilespec.FieldManager.String()
	transcodeProfileName = "hevc-main10"
	transcodeProfileHash = "profile-hash-def"
	transcodeTag         = transcodeProfileName + "@" + transcodeProfileHash
)

// probeHarness is the shared probe harness.
type probeHarness = remediationtest.ProbeHarness

// specFields is the spec fields manager claims on mf's main resource.
func specFields(mf catalogv1alpha1.MediaFile, manager string) map[string]bool {
	return remediationtest.SpecFieldNames(managedFieldPaths(mf.ManagedFields, manager, ""))
}

// importCreates stands in for the import domain's materialisation of a
// placed file: the whole MediaFileSpec under its own field manager.
func importCreates(t *testing.T, c client.Client, ns, name string, ref commonv1.MediaRef, path string, size int64, mod time.Time, q commonv1.Quality) {
	t.Helper()
	ac := catalogac.MediaFile(name, ns).WithSpec(catalogac.MediaFileSpec().
		WithMediaRef(ref).WithPath(path).WithSizeBytes(size).WithModTime(metav1.NewTime(mod)).
		WithQuality(q).WithRevision(commonv1.Revision{Version: 1}).WithReleaseType(commonv1.ReleaseTypeSingle).
		WithFormatScore(40).WithProfileHash("profile-hash-abc").WithOriginal(true))
	_, err := k8s.Apply(context.Background(), c, mediafilespec.FieldManager, ac)
	require.NoError(t, err)
}

// queuedFile is a movie MediaFile the import domain created over a stand-in
// file, before any probe, on its own envtest apiserver.
type queuedFile struct {
	e    *remediationtest.Env
	c    client.Client
	key  types.NamespacedName
	path string
}

func newQueuedFile(t *testing.T, ns string) queuedFile {
	t.Helper()
	return newQueuedFileAt(t, ns, "Inception (2010).mkv", []byte("stand-in bytes"), bluray1080p)
}

func newQueuedFileAt(t *testing.T, ns, file string, contents []byte, q commonv1.Quality) queuedFile {
	t.Helper()
	e := remediationtest.Start(t)
	ctx := context.Background()
	mustNamespace(t, ctx, e.Admin, ns)
	mustQualityProfile(t, ctx, e.Admin, "qp-video")
	mustMovie(t, ctx, e.Admin, ns, "inception", "qp-video")
	path := writeFile(t, t.TempDir(), file, contents)
	stat, err := os.Stat(path)
	require.NoError(t, err)
	const name = "inception-abc1234567"
	importCreates(t, e.Admin, ns, name, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"},
		path, stat.Size(), stat.ModTime(), q)
	return queuedFile{e: e, c: e.Admin, key: types.NamespacedName{Namespace: ns, Name: name}, path: path}
}

func (f queuedFile) get(t *testing.T) catalogv1alpha1.MediaFile {
	t.Helper()
	return *f.e.Get(t, f.key)
}

func (f queuedFile) uid(t *testing.T) string { return string(f.get(t).UID) }

func reasonOf(mf catalogv1alpha1.MediaFile, typ string) string {
	if c := k8s.FindCondition(mf.Status.Conditions, typ); c != nil {
		return c.Reason
	}
	return ""
}

// setStatus changes f's stored status as another planner of the loop would:
// the whole status, mutated, applied under catalog.
func setStatus(t *testing.T, f queuedFile, mutate func(*catalogv1alpha1.MediaFileStatus)) {
	t.Helper()
	mf := f.get(t)
	st := mf.Status.DeepCopy()
	mutate(st)
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	var ac catalogac.MediaFileStatusApplyConfiguration
	require.NoError(t, json.Unmarshal(raw, &ac))
	_, err = k8s.PatchStatus(context.Background(), f.c, k8s.ManagerCatalog,
		catalogac.MediaFile(f.key.Name, f.key.Namespace).WithStatus(&ac))
	require.NoError(t, err)
}

// swapping is the transcode planner's block once a worker answered that it
// swapped output in (loop spec §5.9): Swapping, its result, finished at.
func swapping(output string, size int64, finished time.Time) *catalogv1alpha1.TranscodeState {
	at := metav1.NewTime(finished.UTC().Truncate(time.Second))
	return &catalogv1alpha1.TranscodeState{
		Phase: catalogv1alpha1.TranscodePhaseSwapping, Profile: transcodeProfileName, ProfileHash: transcodeProfileHash,
		FinishedAt: &at, Result: &catalogv1alpha1.TranscodeOutput{OutputPath: output, OutputSizeBytes: size},
	}
}

// encodeOf is what the probe of a swapped-in encode reads: the tag the
// worker wrote (CLUSTARR_PROFILE), on the container its name says.
func encodeOf(path string) *commonv1.MediaInfo {
	mi := fakeMediaInfo()
	mi.VideoCodec, mi.TranscodeProfile = "hevc", transcodeTag
	mi.Container = strings.TrimPrefix(path[strings.LastIndex(path, "."):], ".")
	return mi
}

// clock is a pass clock a test moves by hand.
type clock struct{ now time.Time }

func newClock() *clock { return &clock{now: time.Now().UTC().Truncate(time.Second)} }

func (c *clock) Now() time.Time      { return c.now }
func (c *clock) Add(d time.Duration) { c.now = c.now.Add(d) }
