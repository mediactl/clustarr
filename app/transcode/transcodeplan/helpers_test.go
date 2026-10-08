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

package transcodeplan

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

const source = "/data/media/movies/Heat (1995)/Heat (1995).mkv"

// h264Probe is the summary catalog stores for an 8-bit 1080p H.264 film
// with one English AC-3 5.1 track: a file the standard encodes.
func h264Probe() *commonv1.MediaInfo {
	return &commonv1.MediaInfo{
		Container: "matroska", VideoCodec: "h264", VideoProfile: "High", PixelFormat: "yuv420p",
		VideoBitDepth: 8, Width: 1920, Height: 1080, FpsMilli: 23976, Hdr: commonv1.HdrFormatNone,
		RuntimeMillis: 2 * 60 * 60 * 1000,
		Audio:         []commonv1.AudioStream{{Index: 1, Codec: "ac3", Channels: 6, Language: "eng", Default: true}},
	}
}

func newProfile(name string, def bool, created time.Time, sel map[string]string) transcodev1alpha1.TranscodeProfile {
	p := transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{
		Name: name, UID: types.UID("uid-" + name), CreationTimestamp: metav1.NewTime(created),
	}}
	p.Spec.Default = def
	if sel != nil {
		p.Spec.Selector = &metav1.LabelSelector{MatchLabels: sel}
	}
	return p
}

// probed is a draft status whose probe of hash is current.
func probed(hash string) *catalogv1alpha1.MediaFileStatus {
	return &catalogv1alpha1.MediaFileStatus{ProbeHash: hash, ProbedAt: new(metav1.NewTime(t0.Add(-time.Hour))), MediaInfo: h264Probe()}
}

func heatFile(st *catalogv1alpha1.MediaFileStatus) *catalogv1alpha1.MediaFile {
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat-1995", UID: "a1b2c3d4-0000-4000-8000-000000000001"},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat-1995"},
			Path:     source, SizeBytes: 8 << 30,
		},
	}
	if st != nil {
		mf.Status = *st
	}
	return mf
}

func eligibleFile(st *catalogv1alpha1.MediaFileStatus) File {
	return File{MF: heatFile(st), Status: st, ProbeCurrent: true, ItemExists: true}
}

// inputs is one pass's Inputs for a movie file whose draft is draft and
// whose stored block is prev, under the default profile hevc, holding
// grant: Eligibility is the real Eligible's.
func inputs(prev *catalogv1alpha1.TranscodeState, draft *catalogv1alpha1.MediaFileStatus, grant GrantKind) Inputs {
	hevc := newProfile("hevc", true, t0, nil)
	f := eligibleFile(draft)
	f.MF.Status.Transcode = prev
	return Inputs{
		File: f.MF, Prev: prev, Draft: draft, ProbeCurrent: true,
		Eligibility: Eligible(f, []transcodev1alpha1.TranscodeProfile{hevc}),
		Ledger:      Ledger{Grant: grant}, Now: metav1.NewTime(t0),
	}
}

// dispatchIn is a Planned file holding a cpu dispatch grant, with the
// movies RootFolder and a view issuer whose floor is 41.
func dispatchIn(t *testing.T) Inputs {
	t.Helper()
	in := inputs(nil, probed("ph-1"), GrantWindow)
	in.Prev = Plan(in).Block // Planned
	if in.Prev == nil || in.Prev.Phase != catalogv1alpha1.TranscodePhasePlanned {
		t.Fatalf("the window grant did not plan the file: %+v", in.Prev)
	}
	in.Ledger = Ledger{Grant: GrantDispatch, Class: transcodev1alpha1.HardwareCPU}
	in.Folders = []catalogv1alpha1.RootFolder{{Spec: catalogv1alpha1.RootFolderSpec{Path: "/data/media/movies"}}}
	floor := int64(41)
	in.Issue = func(rec int64) int64 { floor = max(floor+1, rec+1); return floor }
	return in
}

// queuedIn is dispatchIn's file after its dispatch landed: Queued at 42,
// the record requested at 42, the profile in Profiles.
func queuedIn(t *testing.T) Inputs {
	t.Helper()
	in := dispatchIn(t)
	q := Plan(in)
	if q.Block == nil || q.Block.Phase != catalogv1alpha1.TranscodePhaseQueued || q.Request == nil {
		t.Fatalf("the dispatch grant did not queue the file: %+v", q.Block)
	}
	in.Prev, in.Ledger, in.Record = q.Block, Ledger{}, q.Request
	in.Profiles = map[string]*transcodev1alpha1.TranscodeProfile{"hevc": in.Eligibility.Profile}
	return in
}

func actions(h []HistoryEvent) []string {
	out := make([]string, 0, len(h))
	for _, e := range h {
		out = append(out, e.Action)
	}
	return out
}

func reasons(ev []Event) []string {
	out := make([]string, 0, len(ev))
	for _, e := range ev {
		out = append(out, e.Reason)
	}
	return out
}

func tagOf(p transcodev1alpha1.TranscodeProfile) string { return Tag(p.Name, jobspec.ProfileHash(p.Spec)) }
