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
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// A file moved with its series from one root folder to another (anime
// library design, 2026-11-25, §8a), or renamed, is the same bytes under a
// new probe hash, the hash naming the path. The probe takes the new hash
// without probing and keeps the one the bytes had, so everything recorded
// under it is still read as this file's; a second move keeps both, a move
// back to a path it had drops that path's, and new bytes drop them all,
// since no earlier hash names those.
func TestAPathOnlyChangeKeepsTheHashTheBytesHad(t *testing.T) {
	const size = 1 << 30
	mod := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	t0 := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	tv := "/data/media/tv/86 EIGHTY-SIX (2021)/Season 01/86 EIGHTY-SIX (2021) - S01E01.mkv"
	anime := "/data/media/anime/86 EIGHTY-SIX (2021)/Season 01/86 EIGHTY-SIX (2021) - S01E01.mkv"
	renamed := "/data/media/anime/86 EIGHTY-SIX (2021)/Season 01/86 EIGHTY-SIX (2021) - S01E01 - Undertaker.mkv"
	hash := func(p string) string { return evaluateProbe(p, size, mod, "").Hash }

	info := &commonv1.MediaInfo{Container: "mkv", VideoCodec: "hevc"}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "eighty-six-s01e01", Namespace: "clustarr-system", UID: "uid-86"},
		Spec:       catalogv1alpha1.MediaFileSpec{Path: tv, SizeBytes: size, ModTime: metav1.NewTime(mod)},
		Status: catalogv1alpha1.MediaFileStatus{
			ProbeHash: hash(tv), ProbeVersion: mediainfo.ProbeVersion, ProbedAt: &metav1.Time{Time: t0}, MediaInfo: info,
		},
	}
	// move is one pass after spec.path became to: what the relocation's
	// rewrite, or a rename, leaves the probe planner to find.
	move := func(to string) ProbeOutcome {
		mf.Spec.Path = to
		out := mf.Status.DeepCopy()
		o := PlanProbe(ProbeInput{
			File: mf, Prev: &mf.Status, Path: to, Info: statInfo{size, mod}, Read: true, Now: metav1.NewTime(t0.Add(time.Hour)),
		}, out)
		mf.Status = *out
		return o
	}

	o := move(anime)
	require.Nil(t, o.Request, "the bytes are the ones status describes: nothing is probed")
	assert.Equal(t, hash(anime), mf.Status.ProbeHash)
	assert.Equal(t, []string{hash(tv)}, mf.Status.FormerProbeHashes)
	assert.True(t, mf.Status.SameBytes(hash(tv)), "what is recorded under the old hash is this file's")
	assert.Equal(t, "hevc", mf.Status.MediaInfo.VideoCodec, "the summary is kept")

	move(renamed)
	assert.Equal(t, []string{hash(tv), hash(anime)}, mf.Status.FormerProbeHashes, "oldest first")

	move(tv)
	assert.Equal(t, hash(tv), mf.Status.ProbeHash)
	assert.Equal(t, []string{hash(anime), hash(renamed)}, mf.Status.FormerProbeHashes,
		"the hash it has again is not a former one")

	// New bytes at the same path: a probe is asked for, and its answer,
	// once taken, describes bytes no earlier hash names.
	const grown = size + 4096
	later := mod.Add(48 * time.Hour)
	newHash := evaluateProbe(tv, grown, later, "").Hash
	in := ProbeInput{
		File: mf, Prev: &mf.Status, Path: tv, Info: statInfo{grown, later}, Read: true, Now: metav1.NewTime(t0.Add(2 * time.Hour)),
	}
	out := mf.Status.DeepCopy()
	o = PlanProbe(in, out)
	require.NotNil(t, o.Request, "changed bytes are probed")
	assert.Equal(t, []string{hash(anime), hash(renamed)}, out.FormerProbeHashes, "kept while status still describes the old bytes")
	ref := schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: string(mf.UID)}
	in.Record = probestore.Current{OK: true, Record: schema.ProbeRecord{
		RecordHeader: schema.RecordHeader{MediaFile: ref, State: schema.ProbeProbed, RequestedAt: in.Now.Time},
		Path:         tv, ProbeHash: newHash, ProbeVersion: mediainfo.ProbeVersion, RequestedVersion: mediainfo.ProbeVersion,
		ProbedAt: in.Now.Time, MediaInfo: &commonv1.MediaInfo{Container: "mkv", VideoCodec: "av1"},
	}}
	in.Now = metav1.NewTime(in.Now.Add(time.Minute))
	out = mf.Status.DeepCopy()
	PlanProbe(in, out)
	require.Equal(t, newHash, out.ProbeHash, "the probe of the new bytes is taken")
	assert.Empty(t, out.FormerProbeHashes)
	assert.False(t, out.SameBytes(hash(anime)))
}
