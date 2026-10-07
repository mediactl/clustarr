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

package mediafilespec_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// renameSpecLeaves is every MediaFileSpec field importarr's complete render
// sends for a steady, imported file. A rename re-applies all of them, so
// every one must still be owned by mediafilespec.FieldManager afterwards.
var renameSpecLeaves = []string{
	"spec.mediaRef", "spec.path", "spec.sizeBytes", "spec.modTime", "spec.quality", "spec.revision",
	"spec.releaseType", "spec.releaseGroup", "spec.edition", "spec.languages", "spec.importedFrom",
	"spec.formatScore", "spec.matchedFormats", "spec.profileHash", "spec.original",
}

// correctedQuality is the probe-corrected quality catalogarr proposes: the
// name said 1080p, the probe found 2160p.
var correctedQuality = commonv1.Quality{Name: "Bluray-2160p", Source: commonv1.SourceBluray, Resolution: 2160}

// staleFile is one steady MediaFile catalogarr has proposed a new name for.
type staleFile struct {
	name     string
	path     string // spec.path, where the file is
	expected string // status.naming.expectedPath
	sidecar  string // a .en.srt next to the file, listed in status.sidecars
}

// plantStaleFile plants a movie file and its .en.srt sidecar, records the
// file under mediafilespec.FieldManager with every frozen field importarr ever
// sets -- as fileimport would -- and seeds catalogarr's status under
// k8s.ManagerCatalogarr: Ready and Probed True, NamingCurrent False, and a
// status.naming proposing expectedBase in the same folder with the
// probe-corrected quality.
func (f *fixture) plantStaleFile(t *testing.T, ctx context.Context, expectedBase string) staleFile {
	t.Helper()
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-949", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd-bluray-web", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, movie))

	dir := filepath.Join(f.root, "Heat (1995) {tmdb-949}")
	path := filepath.Join(dir, "heat.1995.1080p.bluray.x264-grp.mkv")
	mustWriteFile(t, path, 4096)
	sidecar := filepath.Join(dir, "heat.1995.1080p.bluray.x264-grp.en.srt")
	mustWriteFile(t, sidecar, 64)
	info, err := os.Stat(path)
	require.NoError(t, err)

	name := k8s.ChildName(movie.Name, "mediafile", path)
	_, err = k8s.Apply(ctx, f.c, mediafilespec.FieldManager, catalogac.MediaFile(name, f.ns).WithSpec(
		catalogac.MediaFileSpec().
			WithMediaRef(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}).
			WithPath(path).
			WithSizeBytes(info.Size()).
			WithModTime(metav1.NewTime(info.ModTime())).
			WithQuality(commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}).
			WithRevision(commonv1.Revision{Version: 2, Real: 1, Repack: true}).
			WithReleaseType(commonv1.ReleaseTypeSingle).
			WithReleaseGroup("GRP").
			WithEdition("Director's Cut").
			WithLanguages("en", "fr").
			WithImportedFrom(catalogac.ImportSource().
				WithDownloadRef("heat-dl").
				WithReleaseTitle("Heat.1995.1080p.BluRay.x264-GRP").
				WithIndexerName("fixture").
				WithProtocol(commonv1.ProtocolTorrent).
				WithImportedAt(metav1.NewTime(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))).
				WithManual(true)).
			WithFormatScore(1250).
			WithMatchedFormats("x264", "Repack").
			WithProfileHash("abc123").
			WithOriginal(true)))
	require.NoError(t, err)

	expected := filepath.Join(dir, expectedBase)
	f.seedNamingStatus(t, ctx, name, catalogac.NamingStatus().
		WithExpectedPath(expected).WithCurrent(false).WithQuality(correctedQuality),
		catalogac.Sidecar().WithPath(sidecar).WithLanguage("en"))
	return staleFile{name: name, path: path, expected: expected, sidecar: sidecar}
}

// seedNamingStatus applies catalogarr's status as its MediaFile reconciler
// would for a file it proposes to rename: Ready, Probed and a False
// NamingCurrent, beside naming and the sidecars.
func (f *fixture) seedNamingStatus(t *testing.T, ctx context.Context, name string,
	naming *catalogac.NamingStatusApplyConfiguration, sidecars ...*catalogac.SidecarApplyConfiguration,
) {
	t.Helper()
	now := metav1.Now()
	conditions := []metav1.Condition{
		{Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", Message: "file present and probed", LastTransitionTime: now},
		{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed", Message: "probed", LastTransitionTime: now},
		{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse, Reason: "Stale", Message: "the file's canonical path differs", LastTransitionTime: now},
	}
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, f.ns).WithStatus(
		catalogac.MediaFileStatus().
			WithConditions(k8s.ConditionACs(conditions)...).
			WithNaming(naming).
			WithSidecars(sidecars...)))
	require.NoError(t, err)
}

// read reads a MediaFile past the cache.
func (f *fixture) read(t *testing.T, ctx context.Context, name string) *catalogv1alpha1.MediaFile {
	t.Helper()
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.api(t).Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &mf))
	return &mf
}

// managersOf lists every field manager that owns jsonPath on the main
// resource. Two managers may co-own a field, and a test that means to catch
// an over-claim has to see all of them, not only the first.
func managersOf(t *testing.T, entries []metav1.ManagedFieldsEntry, jsonPath string) []string {
	t.Helper()
	var out []string
	for _, e := range entries {
		if e.Subresource != "" || e.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		if ownsPath(fields, splitPath(jsonPath)) {
			out = append(out, e.Manager)
		}
	}
	return out
}

func requireExists(t *testing.T, path string) {
	t.Helper()
	_, err := os.Stat(path)
	require.NoError(t, err, "%s should exist", path)
}

func requireAbsent(t *testing.T, path string) {
	t.Helper()
	_, err := os.Lstat(path)
	require.True(t, os.IsNotExist(err), "%s should not exist (err %v)", path, err)
}

const renamedBase = "Heat (1995) {tmdb-949} [Bluray-2160p][x264].mkv"

// A rename moves the file and its sidecar, and re-applies importarr's
// COMPLETE spec with the new path and the probe-corrected quality: every
// frozen field survives, and mediafilespec.FieldManager still owns each one --
// the rename is not a second, narrower apply that releases the rest.
func TestRenameFileMovesTheFileAndReappliesTheCompleteSpec(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-move", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	sf := f.plantStaleFile(t, ctx, renamedBase)
	before := f.read(t, ctx, sf.name)

	out, err := mediafilespec.RenameFile(ctx, f.c, f.api(t), before, false, false)
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameOutcome{From: sf.path, To: sf.expected, Moved: true}, out)

	requireAbsent(t, sf.path)
	requireExists(t, sf.expected)
	requireAbsent(t, sf.sidecar)
	requireExists(t, filepath.Join(filepath.Dir(sf.expected), "Heat (1995) {tmdb-949} [Bluray-2160p][x264].en.srt"))

	after := f.read(t, ctx, sf.name)
	want := before.Spec
	want.Path = sf.expected
	want.Quality = correctedQuality
	assert.Equal(t, want, after.Spec, "only path and quality change; every frozen field is re-asserted verbatim")

	for _, leaf := range renameSpecLeaves {
		assert.Equal(t, []string{string(mediafilespec.FieldManager)}, managersOf(t, after.ManagedFields, leaf), leaf)
	}
	for _, leaf := range renameSpecLeaves {
		assert.NotContains(t, managersOf(t, after.ManagedFields, leaf), string(k8s.ManagerImportarr),
			"%s: k8s.ManagerImportarr owns only the observed-fingerprint annotation on a MediaFile", leaf)
	}
}

// A transcoded file's size, mtime and original flag are catalogarr's
// (CLAUDE.md's invariant): the rename moves it and applies the new path,
// but leaves those three to catalogarr alone.
func TestRenameFileLeavesATranscodedFilesTakenOverFieldsToCatalogarr(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-transcoded", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	sf := f.plantStaleFile(t, ctx, renamedBase)
	info, err := os.Stat(sf.path)
	require.NoError(t, err)
	require.NoError(t, takeOver(ctx, f.c, f.ns, sf.name, info.Size(), info.ModTime()))

	out, err := mediafilespec.RenameFile(ctx, f.c, f.api(t), f.read(t, ctx, sf.name), false, false)
	require.NoError(t, err)
	require.True(t, out.Moved, "outcome %+v", out)
	requireExists(t, sf.expected)

	after := f.read(t, ctx, sf.name)
	assert.Equal(t, sf.expected, after.Spec.Path)
	assert.Equal(t, info.Size(), after.Spec.SizeBytes)
	require.NotNil(t, after.Spec.Original)
	assert.False(t, *after.Spec.Original)
	for _, leaf := range []string{"spec.sizeBytes", "spec.modTime", "spec.original"} {
		assert.Equal(t, []string{string(k8s.ManagerCatalogarr)}, managersOf(t, after.ManagedFields, leaf), leaf)
	}
	assert.Contains(t, managersOf(t, after.ManagedFields, "spec.path"), string(mediafilespec.FieldManager))
	assert.Equal(t, []string{string(mediafilespec.FieldManager)}, managersOf(t, after.ManagedFields, "spec.quality"))
}

// A file already at the proposed path is never overwritten.
func TestRenameFileRefusesACollision(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-collision", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	sf := f.plantStaleFile(t, ctx, renamedBase)
	mustWriteFile(t, sf.expected, 1234)
	before := f.read(t, ctx, sf.name)

	out, err := mediafilespec.RenameFile(ctx, f.c, f.api(t), before, false, false)
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameOutcome{From: sf.path, To: sf.expected, Reason: mediafilespec.RenameCollision}, out)

	requireExists(t, sf.path)
	st, err := os.Stat(sf.expected)
	require.NoError(t, err)
	assert.Equal(t, int64(1234), st.Size(), "the file already there is untouched")
	assert.Equal(t, before.ResourceVersion, f.read(t, ctx, sf.name).ResourceVersion, "nothing was applied")
}

// A second writer changes the file between the MediaFile's read and the
// move: the fingerprint no longer matches spec, so nothing moves and the
// outcome is Changed, for the rescan to re-observe.
func TestRenameFileRefusesAFileChangedInFlight(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-changed", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	sf := f.plantStaleFile(t, ctx, renamedBase)
	before := f.read(t, ctx, sf.name)

	restore := mediafilespec.SetRenameBeforeMove(func() { require.NoError(t, os.Truncate(sf.path, 10)) })
	defer restore()

	out, err := mediafilespec.RenameFile(ctx, f.c, f.api(t), before, false, false)
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameOutcome{From: sf.path, To: sf.expected, Reason: mediafilespec.RenameChanged}, out)

	requireExists(t, sf.path)
	requireAbsent(t, sf.expected)
	requireExists(t, sf.sidecar)
	assert.Equal(t, before.ResourceVersion, f.read(t, ctx, sf.name).ResourceVersion, "nothing was applied")
}

// A dry run reports what it would do and touches nothing.
func TestRenameFileDryRunTouchesNothing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-dryrun", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	sf := f.plantStaleFile(t, ctx, renamedBase)
	before := f.read(t, ctx, sf.name)

	out, err := mediafilespec.RenameFile(ctx, f.c, f.api(t), before, true, false)
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameOutcome{From: sf.path, To: sf.expected, Reason: mediafilespec.RenameDryRun}, out)

	requireExists(t, sf.path)
	requireExists(t, sf.sidecar)
	requireAbsent(t, sf.expected)
	assert.Equal(t, before.ResourceVersion, f.read(t, ctx, sf.name).ResourceVersion, "nothing was applied")
}

// The refusals that need no filesystem look: a proposal catalogarr holds, a
// proposal in another folder (files only, spec D5), and a file already at
// its proposal or with none.
func TestRenameFileHoldsWhatItMayNotMove(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-held", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	sf := f.plantStaleFile(t, ctx, renamedBase)
	otherFolder := filepath.Join(f.root, "Heat (1995) {tmdb-949} {edition-Directors Cut}", renamedBase)

	for _, tc := range []struct {
		name   string
		naming *catalogac.NamingStatusApplyConfiguration
		want   string
		to     string
	}{
		{
			name: "catalogarr holds it",
			naming: catalogac.NamingStatus().WithExpectedPath(sf.expected).WithCurrent(false).
				WithReason(catalogv1alpha1.NamingReasonTranscodePending),
			want: mediafilespec.RenameHeld,
		},
		{
			name:   "another folder",
			naming: catalogac.NamingStatus().WithExpectedPath(otherFolder).WithCurrent(false),
			want:   mediafilespec.RenameHeld, to: otherFolder,
		},
		{
			name:   "already current",
			naming: catalogac.NamingStatus().WithExpectedPath(sf.path).WithCurrent(true),
			want:   mediafilespec.RenameNotCurrent,
		},
		{
			name:   "spec.path is the proposal though current still reads false",
			naming: catalogac.NamingStatus().WithExpectedPath(sf.path).WithCurrent(false),
			want:   mediafilespec.RenameNotCurrent,
		},
		{
			name:   "nothing proposed",
			naming: catalogac.NamingStatus().WithCurrent(false).WithReason(catalogv1alpha1.NamingReasonMetadataPending),
			want:   mediafilespec.RenameNotCurrent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.seedNamingStatus(t, ctx, sf.name, tc.naming)
			before := f.read(t, ctx, sf.name)

			out, err := mediafilespec.RenameFile(ctx, f.c, f.api(t), before, false, false)
			require.NoError(t, err)
			assert.Equal(t, tc.want, out.Reason)
			assert.False(t, out.Moved)
			assert.Equal(t, sf.path, out.From)
			if tc.to != "" {
				assert.Equal(t, tc.to, out.To)
			}
			requireExists(t, sf.path)
			assert.Equal(t, before.ResourceVersion, f.read(t, ctx, sf.name).ResourceVersion, "nothing was applied")
		})
	}
}

// A second writer changes the MediaFile itself between the rename's read and
// its apply: the apply's resourceVersion precondition refuses it, and the
// file is moved back, so spec.path never names a path with no file.
func TestRenameFileMovesBackWhenTheMediaFileChangedInFlight(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-conflict", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	sf := f.plantStaleFile(t, ctx, renamedBase)
	before := f.read(t, ctx, sf.name)

	restore := mediafilespec.SetRenameBeforeMove(func() {
		// catalogarr records that captionarr's sidecar is an SDH track.
		f.seedNamingStatus(t, ctx, sf.name, catalogac.NamingStatus().
			WithExpectedPath(sf.expected).WithCurrent(false).WithQuality(correctedQuality),
			catalogac.Sidecar().WithPath(sf.sidecar).WithLanguage("en").WithHI(true))
	})
	defer restore()

	out, err := mediafilespec.RenameFile(ctx, f.c, f.api(t), before, false, false)
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameOutcome{From: sf.path, To: sf.expected, Reason: mediafilespec.RenameChanged}, out)

	requireExists(t, sf.path)
	requireAbsent(t, sf.expected)
	requireExists(t, sf.sidecar)
	after := f.read(t, ctx, sf.name)
	assert.NotEqual(t, before.ResourceVersion, after.ResourceVersion, "the second writer's change landed")
	assert.Equal(t, before.Spec, after.Spec, "the rename applied nothing")
}

// Something appears at the proposed path after the check found it free and
// before the move: the move refuses it (link(2), not rename(2)) instead of
// replacing it, and the outcome is Collision.
func TestRenameFileRefusesATargetThatAppearsBeforeTheMove(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-late-collision", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	sf := f.plantStaleFile(t, ctx, renamedBase)
	before := f.read(t, ctx, sf.name)

	restore := mediafilespec.SetRenameBeforeMove(func() { require.NoError(t, os.WriteFile(sf.expected, []byte("arrived late"), 0o644)) })
	defer restore()

	out, err := mediafilespec.RenameFile(ctx, f.c, f.api(t), before, false, false)
	require.NoError(t, err)
	assert.Equal(t, mediafilespec.RenameOutcome{From: sf.path, To: sf.expected, Reason: mediafilespec.RenameCollision}, out)

	st, err := os.Stat(sf.path)
	require.NoError(t, err)
	assert.Equal(t, before.Spec.SizeBytes, st.Size(), "the file to rename is intact")
	got, err := os.ReadFile(sf.expected)
	require.NoError(t, err)
	assert.Equal(t, "arrived late", string(got), "the file that arrived is not overwritten")
	requireExists(t, sf.sidecar)
	assert.Equal(t, before.ResourceVersion, f.read(t, ctx, sf.name).ResourceVersion, "nothing was applied")
}

// A rename that moved the file and died before its apply -- spec.path is
// gone, the proposed path holds a file with spec's fingerprint -- is rolled
// forward: the apply is made without a move. A move cut short between its
// link and its unlink (both names, one file) is finished. A file at the
// proposed path that is not the recorded one is a Collision.
func TestRenameFileRollsForwardAnUnrecordedMove(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		crash func(t *testing.T, sf staleFile)
		want  string
	}{
		{
			name:  "moved-not-applied",
			crash: func(t *testing.T, sf staleFile) { require.NoError(t, os.Rename(sf.path, sf.expected)) },
		},
		{
			name:  "linked-not-unlinked",
			crash: func(t *testing.T, sf staleFile) { require.NoError(t, os.Link(sf.path, sf.expected)) },
		},
		{
			name: "another-file-there",
			crash: func(t *testing.T, sf staleFile) {
				require.NoError(t, os.Rename(sf.path, sf.expected))
				require.NoError(t, os.Truncate(sf.expected, 10))
			},
			want: mediafilespec.RenameCollision,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, ctx, "rn-forward-"+tc.name, catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
			sf := f.plantStaleFile(t, ctx, renamedBase)
			before := f.read(t, ctx, sf.name)
			tc.crash(t, sf)

			out, err := mediafilespec.RenameFile(ctx, f.c, f.api(t), before, false, false)
			require.NoError(t, err)
			after := f.read(t, ctx, sf.name)
			if tc.want != "" {
				assert.Equal(t, mediafilespec.RenameOutcome{From: sf.path, To: sf.expected, Reason: tc.want}, out)
				assert.Equal(t, before.ResourceVersion, after.ResourceVersion, "nothing was applied")
				return
			}
			assert.Equal(t, mediafilespec.RenameOutcome{From: sf.path, To: sf.expected, Moved: true}, out)
			requireAbsent(t, sf.path)
			requireExists(t, sf.expected)
			requireExists(t, filepath.Join(filepath.Dir(sf.expected), "Heat (1995) {tmdb-949} [Bluray-2160p][x264].en.srt"))
			want := before.Spec
			want.Path = sf.expected
			want.Quality = correctedQuality
			assert.Equal(t, want, after.Spec)
		})
	}
}
