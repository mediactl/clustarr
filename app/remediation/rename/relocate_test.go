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

package rename

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// relocateRig is a Series moved to the anime root -- spec.rootFolderRef
// changed, its reconciler having rendered its folder there -- with one
// episode's file, and its subtitle, still in the folder it had under the TV
// root. Neither root folder renames anything: a relocation is not a rename
// the root's switches decide.
type relocateRig struct {
	c                  client.Client
	a                  *Actuator
	rec                *k8sevents.FakeRecorder
	env                *remediation.Env
	mf                 *catalogv1alpha1.MediaFile
	tv, anime          string // the two root folders
	from, to           string // the file, and where it goes
	sidecar, sidecarTo string
}

func newRelocateRig(t *testing.T, mutate func(g *relocateRig, s *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile), extra ...client.Object) *relocateRig {
	t.Helper()
	g := &relocateRig{tv: t.TempDir(), anime: t.TempDir(), rec: k8sevents.NewFakeRecorder(16)}
	const folder, season, name = "86 EIGHTY-SIX (2021)", "Season 01", "[Group] 86 - 01 (1080p)"
	g.from = filepath.Join(g.tv, folder, season, name+".mkv")
	g.to = filepath.Join(g.anime, folder, season, name+".mkv")
	g.sidecar, g.sidecarTo = filepath.Join(g.tv, folder, season, name+".en.srt"), filepath.Join(g.anime, folder, season, name+".en.srt")
	require.NoError(t, os.MkdirAll(filepath.Dir(g.from), 0o755))
	require.NoError(t, os.WriteFile(g.from, []byte("matroska"), 0o644))
	require.NoError(t, os.WriteFile(g.sidecar, []byte("1\n"), 0o644))
	info, err := os.Stat(g.from)
	require.NoError(t, err)

	root := func(name, path string, typ catalogv1alpha1.SeriesType) *catalogv1alpha1.RootFolder {
		rf := &catalogv1alpha1.RootFolder{ObjectMeta: objMeta(name), Spec: catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindSeries}}
		rf.Spec.Defaults.SeriesType = typ
		return rf
	}
	s := &catalogv1alpha1.Series{
		ObjectMeta: objMeta("eighty-six"),
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 378609, QualityProfileRef: "q", RootFolderRef: "anime", SeriesType: catalogv1alpha1.SeriesTypeAnime},
		Status:     catalogv1alpha1.SeriesStatus{Path: filepath.Join(g.anime, folder)},
	}
	g.mf = &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "eighty-six-s01e01", UID: "uid-86-s01e01"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "eighty-six-s01e01"},
			Path:     g.from, SizeBytes: info.Size(), ModTime: metav1.NewTime(info.ModTime()),
		},
		Status: catalogv1alpha1.MediaFileStatus{
			// What the naming planner renders from the Series' folder: a
			// name under the anime root, which no root's switch applies.
			Naming:   &catalogv1alpha1.NamingStatus{ExpectedPath: filepath.Join(g.anime, folder, season, "86 EIGHTY-SIX (2021) - S01E01.mkv")},
			Sidecars: []catalogv1alpha1.Sidecar{{Name: name + ".en.srt", Language: "en"}},
			Conditions: []metav1.Condition{
				{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse, Reason: "Stale"},
				{Type: catalogv1alpha1.MediaFileConditionReady, Status: metav1.ConditionTrue, Reason: "Ready"},
				{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed"},
			},
		},
	}
	if mutate != nil {
		mutate(g, s, g.mf)
	}
	objs := append([]client.Object{
		root("tv", g.tv, catalogv1alpha1.SeriesTypeStandard), root("anime", g.anime, catalogv1alpha1.SeriesTypeAnime),
		s, &catalogv1alpha1.Episode{ObjectMeta: objMeta("eighty-six-s01e01"), Spec: catalogv1alpha1.EpisodeSpec{SeriesRef: "eighty-six", SeasonNumber: 1, EpisodeNumber: 1}},
		g.mf,
	}, extra...)
	g.c = fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithStatusSubresource(&catalogv1alpha1.MediaFile{}).WithObjects(objs...).Build()
	g.a = New(Options{Recorder: g.rec})
	g.env = &remediation.Env{Reader: g.c, APIReader: g.c}
	return g
}

func (g *relocateRig) act(t *testing.T) {
	t.Helper()
	_, err := g.a.Act(t.Context(), g.env, g.c, g.mf)
	require.NoError(t, err)
}

func (g *relocateRig) stored(t *testing.T) *catalogv1alpha1.MediaFile {
	t.Helper()
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, g.c.Get(t.Context(), client.ObjectKeyFromObject(g.mf), &mf))
	return &mf
}

// A Series moved to the anime root is in the Anime library, and a library
// is a folder (anime library design, 2026-11-25, §3): its files follow it,
// each to the place under the Series' new folder that it had under the old
// one, its own name kept, its subtitle with it, and the path recorded in
// the same step. The folders the move empties go -- the Series' old folder
// too, the root folder never.
func TestAFileFollowsItsSeriesToAnotherRootFolder(t *testing.T) {
	g := newRelocateRig(t, nil)
	g.act(t)

	moved := withPrefix(drain(g.rec), "Normal "+ReasonRelocated)
	require.Len(t, moved, 1)
	assert.Contains(t, moved[0], "root folder anime")
	assert.FileExists(t, g.to, "its place below the Series' folder, and its own name, kept")
	assert.NoFileExists(t, g.from)
	assert.FileExists(t, g.sidecarTo)
	assert.NoFileExists(t, g.sidecar)
	assert.Equal(t, g.to, g.stored(t).Spec.Path)
	assert.NoDirExists(t, filepath.Join(g.tv, "86 EIGHTY-SIX (2021)"), "the folder it emptied is pruned")
	assert.DirExists(t, g.tv)

	// Moved: the next pass has nothing to relocate, and no root folder
	// here renames, so nothing else happens either.
	g.mf = g.stored(t)
	g.act(t)
	assert.Empty(t, drain(g.rec))
	assert.FileExists(t, g.to)
}

// What is not a relocation, and is left exactly where it is: a file under
// its Series' root folder; a file whose Series was re-rooted but whose
// folder under the new root its reconciler has not rendered yet; a film,
// which moves with nothing; and a file with no name proposed, which is
// still read through its Series and moved.
func TestOnlyAFileUnderAnotherRootFolderThanItsSeriesIsMoved(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(g *relocateRig, s *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile)
		moves  bool
	}{
		"the series is stored under the root folder the file is in": {func(g *relocateRig, s *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile) {
			s.Spec.RootFolderRef, s.Status.Path = "tv", filepath.Join(g.tv, "86 EIGHTY-SIX (2021)")
			mf.Status.Naming.ExpectedPath = filepath.Join(filepath.Dir(g.from), "86 EIGHTY-SIX (2021) - S01E01.mkv")
		}, false},
		"the series' folder under its new root folder is not rendered yet": {func(g *relocateRig, s *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile) {
			s.Status.Path = filepath.Join(g.tv, "86 EIGHTY-SIX (2021)")
			mf.Status.Naming = nil
		}, false},
		"the series has no folder yet": {func(_ *relocateRig, s *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile) {
			s.Status.Path = ""
			mf.Status.Naming = nil
		}, false},
		"a film": {func(_ *relocateRig, _ *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile) {
			mf.Spec.MediaRef = commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}
		}, false},
		"no name is proposed for the file": {func(_ *relocateRig, _ *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile) {
			mf.Status.Naming = nil
		}, true},
		"the file's name is current": {func(g *relocateRig, _ *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile) {
			mf.Status.Naming.ExpectedPath, mf.Status.Naming.Current = g.to, false
			mf.Status.Conditions[0].Status = metav1.ConditionTrue
		}, true},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRelocateRig(t, tc.mutate)
			g.act(t)
			if tc.moves {
				assert.Len(t, withPrefix(drain(g.rec), "Normal "+ReasonRelocated), 1)
				assert.FileExists(t, g.to)
				assert.NoFileExists(t, g.from)
				return
			}
			assert.Empty(t, withPrefix(drain(g.rec), "Normal "+ReasonRelocated))
			assert.FileExists(t, g.from)
			assert.NoFileExists(t, g.to)
			assert.Equal(t, g.from, g.stored(t).Spec.Path)
		})
	}
}

// A relocation waits as a rename does: for a transcode or a graft of the
// file to end, since its task names the path the file is at; for the file
// to be present and probed; and for a LibraryScan walking either root
// folder, which could re-attribute the vacated path or find the file at its
// new one before the move is recorded.
func TestARelocationWaitsForTheFileToBeAtRestAndForScans(t *testing.T) {
	for name, mutate := range map[string]func(g *relocateRig, s *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile){
		"a transcode is queued": func(_ *relocateRig, _ *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile) {
			mf.Status.Transcode = &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseQueued}
		},
		"a graft is running": func(_ *relocateRig, _ *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile) {
			mf.Status.Graft = &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseRunning}
		},
		"the file is missing": func(_ *relocateRig, _ *catalogv1alpha1.Series, mf *catalogv1alpha1.MediaFile) {
			mf.Status.Conditions[1].Status = metav1.ConditionFalse
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newRelocateRig(t, mutate)
			g.act(t)
			assert.Empty(t, drain(g.rec))
			assert.FileExists(t, g.from)
			assert.NoFileExists(t, g.to)
		})
	}

	for name, rootFolder := range map[string]string{"the root folder it leaves": "tv", "the root folder it moves to": "anime"} {
		t.Run("a scan of "+name, func(t *testing.T) {
			scan := &catalogv1alpha1.LibraryScan{
				ObjectMeta: objMeta("whole-root"),
				Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: rootFolder},
				Status:     catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning},
			}
			g := newRelocateRig(t, nil, scan)
			wake, err := g.a.Act(t.Context(), g.env, g.c, g.mf)
			require.NoError(t, err)
			assert.Equal(t, recheckAfter, wake)
			assert.Len(t, withPrefix(drain(g.rec), "Normal "+ReasonScanInProgress), 1)
			assert.FileExists(t, g.from)
			assert.NoFileExists(t, g.to)

			var done catalogv1alpha1.LibraryScan
			require.NoError(t, g.c.Get(t.Context(), client.ObjectKeyFromObject(scan), &done))
			done.Status.Phase = catalogv1alpha1.ScanPhaseCompleted
			require.NoError(t, g.c.Update(t.Context(), &done))
			g.act(t)
			assert.Len(t, withPrefix(drain(g.rec), "Normal "+ReasonRelocated), 1, "it moves once the scan has finished")
			assert.FileExists(t, g.to)
		})
	}
}

// Something already at the file's place under the new folder is never
// replaced -- two series' folders of one name are not merged over each
// other -- and is said once, not on every pass.
func TestARelocationNeverReplacesWhatIsInItsWay(t *testing.T) {
	g := newRelocateRig(t, nil)
	require.NoError(t, os.MkdirAll(filepath.Dir(g.to), 0o755))
	require.NoError(t, os.WriteFile(g.to, []byte("another series' episode"), 0o644))

	g.act(t)
	assert.Len(t, withPrefix(drain(g.rec), "Warning Collision"), 1)
	g.act(t)
	assert.Empty(t, drain(g.rec), "remembered until it is due again")
	assert.FileExists(t, g.from)
	b, err := os.ReadFile(g.to)
	require.NoError(t, err)
	assert.Equal(t, "another series' episode", string(b))
	assert.Equal(t, g.from, g.stored(t).Spec.Path)
}
