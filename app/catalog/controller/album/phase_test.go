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

package album_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// The Album's phase is rollup.LeafPhase over the Album's own facts
// (2026-11-10 design D4): no fresh metadata reads Pending, the release
// group's date gates Unavailable, the pending grab reads Delayed, the
// profile decides CutoffUnevaluated, and a track without a file holds the
// album below its cutoff whatever its files' quality (design D6).
func TestPhaseInputsReadTheAlbum(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	fresh := func(release *metav1.Time) *catalogv1alpha1.AlbumMetadata {
		return &catalogv1alpha1.AlbumMetadata{Title: "OK Computer", ReleaseDate: release, RefreshedAt: metav1.NewTime(now.Add(-time.Hour)), SchemaVersion: pkgmetadata.SchemaVersion}
	}
	dated := func(d int) *metav1.Time { t := metav1.NewTime(now.AddDate(0, 0, d)); return &t }
	grab := &catalogv1alpha1.PendingGrab{}
	cases := []struct {
		name                                                                         string
		status                                                                       catalogv1alpha1.AlbumStatus
		monitored, metadataReady, hasFile, cutoffMet, profileResolved, tracksMissing bool
		phase                                                                        commonv1.LeafPhase
		reason                                                                       string
	}{
		{"unmonitored outranks everything", catalogv1alpha1.AlbumStatus{PendingGrab: grab}, false, true, true, true, true, false, catalogv1alpha1.AlbumPhaseUnmonitored, commonv1.CutoffReasonCutoffMet},
		{"no metadata yet is Pending", catalogv1alpha1.AlbumStatus{}, true, false, false, false, true, false, catalogv1alpha1.AlbumPhasePending, commonv1.CutoffReasonPending},
		{"fresh metadata, undated, nothing yet: Wanted (Lidarr decides undated ones)", catalogv1alpha1.AlbumStatus{Metadata: fresh(nil)}, true, true, false, false, true, false, catalogv1alpha1.AlbumPhaseWanted, commonv1.CutoffReasonPending},
		{"released, nothing yet: Wanted", catalogv1alpha1.AlbumStatus{Metadata: fresh(dated(-30))}, true, true, false, false, true, false, catalogv1alpha1.AlbumPhaseWanted, commonv1.CutoffReasonPending},
		{"not yet released: Unavailable", catalogv1alpha1.AlbumStatus{Metadata: fresh(dated(30))}, true, true, false, false, true, false, catalogv1alpha1.AlbumPhaseUnavailable, commonv1.CutoffReasonPending},
		{"every track on disk at the cutoff: Imported", catalogv1alpha1.AlbumStatus{Metadata: fresh(nil)}, true, true, true, true, true, false, catalogv1alpha1.AlbumPhaseImported, commonv1.CutoffReasonCutoffMet},
		{"imported outranks a pending grab", catalogv1alpha1.AlbumStatus{Metadata: fresh(nil), PendingGrab: grab}, true, true, true, true, true, false, catalogv1alpha1.AlbumPhaseImported, commonv1.CutoffReasonCutoffMet},
		{"a track missing holds the album below its cutoff whatever its files' quality", catalogv1alpha1.AlbumStatus{Metadata: fresh(nil)}, true, true, true, true, true, true, catalogv1alpha1.AlbumPhaseCutoffUnmet, commonv1.CutoffReasonTracksMissing},
		{"a file below the cutoff", catalogv1alpha1.AlbumStatus{Metadata: fresh(nil)}, true, true, true, false, true, false, catalogv1alpha1.AlbumPhaseCutoffUnmet, commonv1.CutoffReasonCutoffUnmet},
		{"files and no profile to judge them", catalogv1alpha1.AlbumStatus{Metadata: fresh(nil)}, true, true, true, false, false, false, catalogv1alpha1.AlbumPhaseCutoffUnevaluated, commonv1.CutoffReasonProfileUnresolved},
		{"a pending grab reads Delayed", catalogv1alpha1.AlbumStatus{Metadata: fresh(nil), PendingGrab: grab}, true, true, false, false, true, false, catalogv1alpha1.AlbumPhaseDelayed, commonv1.CutoffReasonPending},
		{"a pending grab outranks a merely cutoff-unmet file", catalogv1alpha1.AlbumStatus{Metadata: fresh(nil), PendingGrab: grab}, true, true, true, false, true, false, catalogv1alpha1.AlbumPhaseDelayed, commonv1.CutoffReasonCutoffUnmet},
		{"a pending grab for the missing tracks reads Delayed", catalogv1alpha1.AlbumStatus{Metadata: fresh(nil), PendingGrab: grab}, true, true, true, true, true, true, catalogv1alpha1.AlbumPhaseDelayed, commonv1.CutoffReasonTracksMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			alb := &catalogv1alpha1.Album{Status: c.status}
			phase, reason := rollup.LeafPhase(album.PhaseInputs(alb, c.monitored, c.metadataReady, c.hasFile, c.cutoffMet, c.profileResolved, c.tracksMissing, now))
			assert.Equal(t, c.phase, phase)
			assert.Equal(t, c.reason, reason)
		})
	}
}

// TracksMissing and TrackSummary read the listing the pass declares: a
// track without a fileRef is missing, no listing misses nothing and
// summarises to nothing.
func TestTracksMissingAndTrackSummaryReadTheListing(t *testing.T) {
	track := func(id string, file string) *catalogac.TrackApplyConfiguration {
		tc := catalogac.Track().WithRecordingID(id)
		if file != "" {
			tc = tc.WithFileRef(file)
		}
		return tc
	}
	assert.False(t, album.TracksMissing(nil), "no release selected: nothing says what the album should hold")
	assert.Empty(t, album.TrackSummary(nil))

	full := []*catalogac.TrackApplyConfiguration{track("r1", "f1"), track("r2", "f2")}
	assert.False(t, album.TracksMissing(full))
	assert.Equal(t, "2/2", album.TrackSummary(full))

	partial := []*catalogac.TrackApplyConfiguration{track("r1", "f1"), track("r2", ""), track("r3", "")}
	assert.True(t, album.TracksMissing(partial))
	assert.Equal(t, "1/3", album.TrackSummary(partial))

	none := []*catalogac.TrackApplyConfiguration{track("r1", "")}
	assert.True(t, album.TracksMissing(none))
	assert.Equal(t, "0/1", album.TrackSummary(none))
}

// MetadataStale is the one staleness rule the pass and the start-time skip
// share.
func TestMetadataStaleIsTheAlbumsRefreshRule(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ttl := pkgmetadata.RefreshTTL(commonv1.MediaKindAlbum, pkgmetadata.RefreshStateActive, now)
	md := func(age time.Duration, schema int32) *catalogv1alpha1.AlbumMetadata {
		return &catalogv1alpha1.AlbumMetadata{RefreshedAt: metav1.NewTime(now.Add(-age)), SchemaVersion: schema}
	}
	cases := []struct {
		name            string
		md              *catalogv1alpha1.AlbumMetadata
		stale, outdated bool
	}{
		{"no document", nil, true, false},
		{"fresh", md(time.Hour, pkgmetadata.SchemaVersion), false, false},
		{"past its TTL", md(ttl+time.Minute, pkgmetadata.SchemaVersion), true, false},
		{"recent but written before the gateway learned a field", md(time.Hour, pkgmetadata.SchemaVersion-1), false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stale, outdated := album.MetadataStale(&catalogv1alpha1.Album{Status: catalogv1alpha1.AlbumStatus{Metadata: c.md}}, now)
			assert.Equal(t, c.stale, stale, "stale")
			assert.Equal(t, c.outdated, outdated, "outdated")
		})
	}
}

// The Album watch's SkipInitial reads the Album as the cache lists it, a
// refresh due whenever the pass would ask for one.
func TestIdleInputsReadTheAlbum(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ref := "ok-computer-dl"
	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "ok-computer", Generation: 5},
		Status: catalogv1alpha1.AlbumStatus{
			ObservedGeneration: 5,
			Phase:              catalogv1alpha1.AlbumPhaseImported,
			Metadata:           &catalogv1alpha1.AlbumMetadata{RefreshedAt: metav1.NewTime(now.Add(-time.Hour)), SchemaVersion: pkgmetadata.SchemaVersion},
			Conditions:         []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}, {Type: "TracksSynced", Status: metav1.ConditionTrue}},
		},
	}
	assert.True(t, rollup.LeafIdleAtStart(album.IdleInputs(alb, true, now)), "imported, fresh, monitored")
	assert.False(t, rollup.LeafIdleAtStart(album.IdleInputs(alb, false, now)), "unmonitored now: the pass moves it")

	stale := alb.DeepCopy()
	stale.Status.Metadata = nil
	assert.True(t, album.IdleInputs(stale, true, now).RefreshDue)
	assert.False(t, rollup.LeafIdleAtStart(album.IdleInputs(stale, true, now)), "a refresh due is work")

	live := alb.DeepCopy()
	live.Status.ActiveDownloadRef, live.Status.DownloadPhase = &ref, commonv1.DownloadPhaseDownloading
	assert.False(t, rollup.LeafIdleAtStart(album.IdleInputs(live, true, now)), "a grab in flight")

	unobserved := alb.DeepCopy()
	unobserved.Generation = 6
	assert.False(t, rollup.LeafIdleAtStart(album.IdleInputs(unobserved, true, now)), "a spec not yet observed")
}
