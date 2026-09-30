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

package mediafile

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

func TestEvaluateProbe(t *testing.T) {
	path := "/data/media/movies/Inception (2010)/Inception (2010).mkv"
	mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	fresh := evaluateProbe(path, 1024, mt, "")
	assert.True(t, fresh.Stale, "empty status hash is always stale")
	assert.NotEmpty(t, fresh.Hash)

	same := evaluateProbe(path, 1024, mt, fresh.Hash)
	assert.False(t, same.Stale, "matching hash is not stale")

	changed := evaluateProbe(path, 2048, mt, fresh.Hash)
	assert.True(t, changed.Stale, "size change makes the hash stale")

	assert.True(t, fresh.ModTime.Time.Equal(mt))
	assert.Equal(t, int64(1024), fresh.SizeBytes)
}

// TestProbeDueForAnOlderProbeVersion: a file probed before the probe
// learned a field (videoEncoder, 2026-09-29) is probed again even though
// its bytes -- and so its probeHash -- are unchanged. The hash stays put:
// captionarr and squasharr read a new hash as a different file.
func TestProbeDueForAnOlderProbeVersion(t *testing.T) {
	path := "/data/media/movies/Inception (2010)/Inception (2010).mkv"
	mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	ps := evaluateProbe(path, 1024, mt, "")
	current := ps.Hash
	ps = evaluateProbe(path, 1024, mt, current)
	assert.False(t, ps.Stale)
	assert.True(t, probeDue(current, 0, ps), "probed by a version that did not record everything")
	assert.False(t, probeDue(current, mediainfo.ProbeVersion, ps), "probed by this version, unchanged")
	assert.True(t, probeDue("", mediainfo.ProbeVersion, ps), "never probed")
	assert.Positive(t, mediainfo.ProbeVersion)
}

// A transcoded file renamed in place (naming.renameTranscoded) moved, it did
// not change: the probe hash names the path, so it is stale, but the size
// and mtime catalogarr recorded are the file's still. Reading the rename as
// a change cleared status.transcode, and the file no longer counted as
// transcoded, so it was never renamed again (Bluey S03E20, 2026-09-30).
func TestAMoveIsNotAChangeOfTheBytes(t *testing.T) {
	mod := time.Date(2026, 9, 30, 11, 39, 0, 0, time.UTC)
	mf := &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{
		Path: "/data/tv/Bluey/Season 3/old.mkv", SizeBytes: 420 << 20, ModTime: metav1.NewTime(mod),
	}}
	old := evaluateProbe(mf.Spec.Path, mf.Spec.SizeBytes, mod, "")
	mf.Status.ProbeHash = old.Hash

	moved := evaluateProbe("/data/tv/Bluey/Season 3/new.mkv", 420<<20, mod, mf.Status.ProbeHash)
	require.True(t, moved.Stale, "fixture: a new path is a new probe hash")
	assert.False(t, bytesChanged(mf, moved), "a rename keeps size and mtime")

	assert.True(t, bytesChanged(mf, evaluateProbe(mf.Spec.Path, 421<<20, mod, mf.Status.ProbeHash)), "new size")
	assert.True(t, bytesChanged(mf, evaluateProbe(mf.Spec.Path, 420<<20, mod.Add(time.Hour), mf.Status.ProbeHash)), "new mtime")
}
