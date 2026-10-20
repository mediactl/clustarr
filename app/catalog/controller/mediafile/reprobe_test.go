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
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	clustarrevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// statInfo is a stat of a regular file of size bytes, modified at mod.
type statInfo struct {
	size int64
	mod  time.Time
}

func (s statInfo) Name() string       { return "file.mkv" }
func (s statInfo) Size() int64        { return s.size }
func (s statInfo) Mode() fs.FileMode  { return 0o644 }
func (s statInfo) ModTime() time.Time { return s.mod }
func (s statInfo) IsDir() bool        { return false }
func (s statInfo) Sys() any           { return nil }

// The files whose summaries the old cap of 64 subtitle streams cut kept
// their bytes, so nothing made them due a probe (talos, 2026-10-20):
// catalog.clustarr.io/reprobe asks for a fresh one. The answer already in
// the record is not taken for it; the fresh answer is, and only then is
// the nonce recorded handled, so the next pass asks for nothing.
func TestAReprobeNonceAsksForAFreshProbeAndTakesItsAnswer(t *testing.T) {
	const size = 4 << 30
	path := "/data/media/tv/Prime Target (2025)/Season 01/Prime Target (2025) - S01E01.mkv"
	mod := time.Date(2025, 1, 22, 0, 0, 0, 0, time.UTC)
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	hash := evaluateProbe(path, size, mod, "").Hash
	sixtyFour := &commonv1.MediaInfo{Subtitles: make([]commonv1.SubtitleStream, 64)}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{
			Name: "prime-target-s01e01", Namespace: "clustarr-system", UID: "uid-1",
			Annotations: map[string]string{catalogv1alpha1.AnnotationReprobe: "n1"},
		},
		Spec: catalogv1alpha1.MediaFileSpec{Path: path, SizeBytes: size, ModTime: metav1.NewTime(mod)},
		Status: catalogv1alpha1.MediaFileStatus{
			ProbeHash: hash, ProbeVersion: mediainfo.ProbeVersion, ProbedAt: &metav1.Time{Time: t0}, MediaInfo: sixtyFour,
		},
	}
	ref := schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: string(mf.UID)}
	old := schema.ProbeRecord{
		RecordHeader: schema.RecordHeader{MediaFile: ref, State: schema.ProbeProbed, RequestedAt: t0.Add(-time.Minute)},
		Path:         path, ProbeHash: hash, ProbeVersion: mediainfo.ProbeVersion, RequestedVersion: mediainfo.ProbeVersion,
		ProbedAt: t0.Add(-30 * time.Second), MediaInfo: sixtyFour,
	}
	input := func(rec schema.ProbeRecord, at time.Time) ProbeInput {
		return ProbeInput{
			File: mf, Prev: &mf.Status, Path: path, Info: statInfo{size, mod},
			Record: probestore.Current{Record: rec, OK: true}, Read: true, Now: metav1.NewTime(at),
		}
	}
	pass := func(rec schema.ProbeRecord, at time.Time) (ProbeOutcome, *catalogv1alpha1.MediaFileStatus) {
		out := mf.Status.DeepCopy()
		return PlanProbe(input(rec, at), out), out
	}

	need, low := NeedsProbeRecord(input(old, t0))
	assert.True(t, need, "a pending nonce reads the record")
	assert.False(t, low, "and asks on the high lane")

	t1 := t0.Add(time.Hour)
	o, out := pass(old, t1)
	require.NotNil(t, o.Request, "the answer already in the record is not the fresh probe asked for")
	assert.Equal(t, clustarrevents.PriorityHigh, o.Request.Lane)
	assert.Nil(t, out.HandledNonces, "handled only once the answer is in")
	assert.Len(t, out.MediaInfo.Subtitles, 64)
	assert.True(t, k8sCondition(out, catalogv1alpha1.MediaFileConditionProbed) != "False",
		"the file stays probed while its bytes are probed again")

	asked := probestore.NewRequest(*o.Request, probestore.Current{Record: old, OK: true}, t1)
	o, out = pass(asked, t1.Add(5*time.Second))
	assert.Nil(t, o.Request, "a request in flight is not asked again")
	assert.Nil(t, out.HandledNonces)

	answered := asked
	answered.State, answered.ProbeVersion, answered.ProbedAt = schema.ProbeProbed, mediainfo.ProbeVersion, t1.Add(8*time.Second)
	answered.MediaInfo = &commonv1.MediaInfo{Subtitles: make([]commonv1.SubtitleStream, 100)}
	o, out = pass(answered, t1.Add(10*time.Second))
	assert.Nil(t, o.Request)
	assert.Len(t, out.MediaInfo.Subtitles, 100, "the fresh answer is taken")
	require.NotNil(t, out.HandledNonces)
	assert.Equal(t, "n1", out.HandledNonces.Reprobe)
	assert.Equal(t, hash, out.ProbeHash, "the same bytes keep their probe hash")

	mf.Status = *out
	need, _ = NeedsProbeRecord(input(answered, t1.Add(time.Minute)))
	assert.False(t, need, "a handled nonce asks for nothing more")
}

func k8sCondition(st *catalogv1alpha1.MediaFileStatus, typ string) metav1.ConditionStatus {
	for _, c := range st.Conditions {
		if c.Type == typ {
			return c.Status
		}
	}
	return ""
}
