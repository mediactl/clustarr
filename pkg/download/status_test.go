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

package download_test

import (
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// notEngineOwned lists the Download.status fields ApplyStatus must never
// emit: nine belong to k8s.ManagerGrabarr and one, import, to
// k8s.ManagerImportarr. grabarr/status holds the authoritative split; this
// copy is what keeps pkg/download honest without importing a service package.
var notEngineOwned = []string{
	"ObservedGeneration", "Phase", "Engine", "FailureReason", "BlocklistedUntil",
	"StartedAt", "CompletedAt", "SeedGoalMetAt", "Conditions", "Import",
}

// populatedStatus is a Download.status with every engine-owned field set to a
// distinct, non-zero value. Times are truncated to the second because
// metav1.Time serialises at that resolution and the round-trip assertion
// below goes through JSON, exactly as a real apply does.
func populatedStatus() downloadv1alpha1.DownloadStatus {
	eta := int32(90)
	at := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	return downloadv1alpha1.DownloadStatus{
		Stage:           downloadv1alpha1.DownloadStageTransferring,
		DownloadID:      "0123456789abcdef0123456789abcdef01234567",
		OutputPath:      "/data/torrents/movies/example",
		ContentRoot:     "/data/torrents/movies/example",
		Files:           []downloadv1alpha1.DownloadFile{{Path: "example.mkv", SizeBytes: 7 << 30}, {Path: "sample.mkv", SizeBytes: 1 << 20, Skipped: true}},
		TotalBytes:      8 << 30,
		RemainingBytes:  1 << 30,
		DownloadedBytes: 7 << 30,
		UploadedBytes:   3 << 30,
		DownloadRateBps: 12_000_000,
		UploadRateBps:   3_000_000,
		ETASeconds:      &eta,
		ProgressPercent: 87,
		Seeders:         42,
		Peers:           17,
		RatioMilli:      1500,
		SeedTimeSeconds: 7200,
		Health: &downloadv1alpha1.UsenetHealth{
			HealthPercent: 99, CriticalHealthPercent: 91, FailedArticles: 3, TotalArticles: 4096,
		},
		IsEncrypted:         true,
		CanMoveFiles:        true,
		CanBeRemoved:        true,
		Message:             "transferring",
		LastProgressAt:      &at,
		EngineFailureReason: downloadv1alpha1.DownloadFailureMissingArticles,
		SeedGoalReached:     true,
		HealthPaused:        true,
	}
}

// statusFromAC renders an apply configuration back into a DownloadStatus the
// way the apiserver would: through JSON. Anything the configuration left nil
// stays at its zero value here, which is what makes "was this field emitted?"
// answerable by comparing structs.
func statusFromAC(t *testing.T, ac any) downloadv1alpha1.DownloadStatus {
	t.Helper()
	raw, err := json.Marshal(ac)
	require.NoError(t, err)
	var st downloadv1alpha1.DownloadStatus
	require.NoError(t, json.Unmarshal(raw, &st))
	// metav1.Time decodes into the local zone; the instant is what round-
	// trips, not the *time.Location the decoder happened to attach.
	if st.LastProgressAt != nil {
		st.LastProgressAt = &metav1.Time{Time: st.LastProgressAt.UTC()}
	}
	return st
}

// The round trip must be exact ON THE STATUS SIDE, because that is what
// grabarr/status.EngineFields relies on: it seeds a complete declaration by
// reading the live object back through ItemFromStatus and re-rendering it, so
// any lossy field would be silently rewritten to a different value on every
// apply that touched something else.
//
// Comparing whole structs, rather than field by field, is the point: a field
// added to DownloadStatus later and forgotten in both directions shows up
// here as a diff rather than as an assertion nobody wrote.
func TestApplyStatusRoundTripsEveryEngineOwnedFieldExactly(t *testing.T) {
	st := populatedStatus()

	got := statusFromAC(t, download.ApplyStatus(download.ItemFromStatus(st)))

	assert.Equal(t, st, got)
}

// ApplyStatus must emit nothing outside the engine's owned set. If it did,
// every telemetry write would claim a field the grabarr controller owns, and
// because pkg/k8s.PatchStatus always applies with ForceOwnership the
// apiserver would hand it over silently instead of reporting a conflict.
func TestApplyStatusEmitsNothingTheEngineDoesNotOwn(t *testing.T) {
	ac := download.ApplyStatus(download.ItemFromStatus(populatedStatus()))
	v := reflect.ValueOf(ac).Elem()

	for _, name := range notEngineOwned {
		f := v.FieldByName(name)
		require.True(t, f.IsValid(), "%s is not a field of DownloadStatusApplyConfiguration", name)
		assert.True(t, f.IsZero(), "ApplyStatus emitted %s, which no grabarr engine owns", name)
	}
}

// Every engine-owned field must be sent on every write, INCLUDING the ones
// whose value is zero. Server-side apply releases what a manager omits, so an
// engine that stopped sending downloadedBytes at 0 -- or canMoveFiles at
// false, the exact shape of a Phase C bug -- would have the apiserver delete
// the field, and it would read back as if the engine had never reported it.
func TestApplyStatusSendsZeroValues(t *testing.T) {
	ac := download.ApplyStatus(download.Item{})
	v := reflect.ValueOf(ac).Elem()
	typ := v.Type()

	// The three pointer fields that are legitimately absent from an empty
	// observation, plus files, which is an empty list rather than a leaf.
	absent := map[string]bool{
		"Stage": true, "ETASeconds": true, "Health": true, "LastProgressAt": true, "Files": true,
		"EngineFailureReason": true,
	}
	notOwned := map[string]bool{}
	for _, n := range notEngineOwned {
		notOwned[n] = true
	}

	for i := range typ.NumField() {
		name := typ.Field(i).Name
		if absent[name] || notOwned[name] {
			continue
		}
		assert.False(t, v.Field(i).IsZero(),
			"ApplyStatus omitted %s for a zero Item; server-side apply would release it", name)
	}
}

// The clamps exist so one arithmetic slip in an engine cannot take down every
// subsequent telemetry write with a CRD validation error.
func TestApplyStatusClampsToTheCRDsBounds(t *testing.T) {
	eta := -5 * time.Second
	got := statusFromAC(t, download.ApplyStatus(download.Item{
		ProgressPercent: 140,
		RatioMilli:      -1,
		Seeders:         -3,
		ETA:             &eta,
		Health:          &downloadv1alpha1.UsenetHealth{HealthPercent: 250, CriticalHealthPercent: -1},
	}))

	assert.EqualValues(t, 100, got.ProgressPercent)
	assert.EqualValues(t, 0, got.RatioMilli)
	assert.EqualValues(t, 0, got.Seeders)
	require.NotNil(t, got.ETASeconds)
	assert.EqualValues(t, 0, *got.ETASeconds)
	require.NotNil(t, got.Health)
	assert.EqualValues(t, 100, got.Health.HealthPercent)
	assert.EqualValues(t, 0, got.Health.CriticalHealthPercent)
}

// status.files carries MaxItems=200 and a discography or full-series pack
// lists more; the apiserver would reject the whole telemetry apply, not just
// the excess, so ApplyStatus renders the first MaxStatusFiles.
func TestApplyStatusCapsFilesAtTheCRDsMaxItems(t *testing.T) {
	files := make([]download.File, download.MaxStatusFiles+37)
	for i := range files {
		files[i] = download.File{Path: "disc/" + strconv.Itoa(i) + ".flac", SizeBytes: 1}
	}
	got := statusFromAC(t, download.ApplyStatus(download.Item{Files: files}))
	require.Len(t, got.Files, download.MaxStatusFiles)
	assert.Equal(t, "disc/0.flac", got.Files[0].Path, "the first files are kept, in order")
}

// ItemFromStatus must not be able to widen the engine's claim: it reads only
// engine-owned fields, so a status full of controller-owned values produces an
// Item that renders to an empty-but-complete engine declaration.
func TestItemFromStatusIgnoresFieldsTheEngineDoesNotOwn(t *testing.T) {
	blocked := metav1.NewTime(time.Now().Truncate(time.Second))
	st := downloadv1alpha1.DownloadStatus{
		ObservedGeneration: 9,
		Phase:              downloadv1alpha1.DownloadPhaseSeeding,
		Engine:             "qbit-0",
		FailureReason:      downloadv1alpha1.DownloadFailureStalled,
		BlocklistedUntil:   &blocked,
		StartedAt:          &blocked,
		CompletedAt:        &blocked,
		SeedGoalMetAt:      &blocked,
		Import:             &downloadv1alpha1.ImportState{State: downloadv1alpha1.ImportPhaseImported},
		Conditions:         []metav1.Condition{{Type: downloadv1alpha1.DownloadConditionAssigned}},
	}

	got := statusFromAC(t, download.ApplyStatus(download.ItemFromStatus(st)))

	assert.Equal(t, downloadv1alpha1.DownloadStatus{}, got,
		"ItemFromStatus carried a field outside the engine's owned set")
}

// A transfer that has not failed must not report a failure. Usenet starts
// every job at DownloadFailureNone, so an ApplyStatus that emitted any
// non-empty reason would tell the controller every usenet download had
// failed with a reason called "none" (gap fix Y2).
func TestApplyStatusReportsAFailureOnlyWhenThereIsOne(t *testing.T) {
	for _, reason := range []downloadv1alpha1.DownloadFailureReason{"", downloadv1alpha1.DownloadFailureNone} {
		got := statusFromAC(t, download.ApplyStatus(download.Item{FailureReason: reason}))
		assert.Emptyf(t, got.EngineFailureReason, "reason %q was reported as a failure", reason)
	}

	got := statusFromAC(t, download.ApplyStatus(download.Item{
		Status:        download.StatusFailed,
		FailureReason: downloadv1alpha1.DownloadFailureDiskFull,
	}))
	assert.Equal(t, downloadv1alpha1.DownloadFailureDiskFull, got.EngineFailureReason)
}
