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
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr/status"
)

// The split, restated as data so that a field added to DownloadStatus later
// cannot quietly belong to nobody.
var (
	controllerOwned = []string{
		"ObservedGeneration", "Phase", "Engine", "FailureReason",
		"BlocklistedUntil", "StartedAt", "CompletedAt", "SeedGoalMetAt", "Conditions",
	}
	engineOwned = []string{
		"Stage", "DownloadID", "OutputPath", "ContentRoot", "Files",
		"TotalBytes", "RemainingBytes", "DownloadedBytes", "UploadedBytes",
		"DownloadRateBps", "UploadRateBps", "ETASeconds", "ProgressPercent",
		"Seeders", "Peers", "RatioMilli", "SeedTimeSeconds", "Health",
		"IsEncrypted", "CanMoveFiles", "CanBeRemoved", "Message", "LastProgressAt",
		"EngineFailureReason", "SeedGoalReached",
	}
	// Written by importarr's file-import worker under k8s.ManagerImportarr.
	// Listed so that "owned by nobody in grabarr" is an assertion rather than
	// an omission.
	foreignOwned = []string{"Import"}
)

// fullStatus sets every field of DownloadStatus to a distinct non-zero value,
// so that "did the declaration send this?" is answerable by looking at whether
// the apply configuration's pointer is nil.
//
// A blank status would make every conditional field look unsent and the test
// would pass whatever the declarations did -- the same shape as a
// release-regression test run against a blank object, which is how this class
// of defect survived three reviews in Phase C.
func fullStatus() downloadv1alpha1.DownloadStatus {
	eta := int32(90)
	at := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	return downloadv1alpha1.DownloadStatus{
		ObservedGeneration:  4,
		Phase:               downloadv1alpha1.DownloadPhaseSeeding,
		Engine:              "torrents-0",
		FailureReason:       downloadv1alpha1.DownloadFailureStalled,
		BlocklistedUntil:    &at,
		StartedAt:           &at,
		CompletedAt:         &at,
		SeedGoalMetAt:       &at,
		Stage:               downloadv1alpha1.DownloadStageSeeding,
		DownloadID:          "0123456789abcdef0123456789abcdef01234567",
		OutputPath:          "/data/torrents/movies/example",
		ContentRoot:         "/data/torrents/movies/example",
		Files:               []downloadv1alpha1.DownloadFile{{Path: "example.mkv", SizeBytes: 7 << 30}},
		TotalBytes:          8 << 30,
		RemainingBytes:      1 << 30,
		DownloadedBytes:     7 << 30,
		UploadedBytes:       3 << 30,
		DownloadRateBps:     12_000_000,
		UploadRateBps:       3_000_000,
		ETASeconds:          &eta,
		ProgressPercent:     87,
		Seeders:             42,
		Peers:               17,
		RatioMilli:          1500,
		SeedTimeSeconds:     7200,
		Health:              &downloadv1alpha1.UsenetHealth{HealthPercent: 99, CriticalHealthPercent: 91, FailedArticles: 3, TotalArticles: 4096},
		IsEncrypted:         true,
		CanMoveFiles:        true,
		CanBeRemoved:        true,
		Message:             "seeding",
		LastProgressAt:      &at,
		EngineFailureReason: downloadv1alpha1.DownloadFailureStalled,
		SeedGoalReached:     true,
		Import:              &downloadv1alpha1.ImportState{State: downloadv1alpha1.ImportPhaseImported},
		Conditions:          []metav1.Condition{{Type: downloadv1alpha1.DownloadConditionAssigned}},
	}
}

// setFields returns the names of every non-nil field of an apply
// configuration.
func setFields(ac any) []string {
	v := reflect.ValueOf(ac).Elem()
	typ := v.Type()
	var out []string
	for i := range typ.NumField() {
		if !v.Field(i).IsZero() {
			out = append(out, typ.Field(i).Name)
		}
	}
	sort.Strings(out)
	return out
}

// Every field of DownloadStatus must be accounted for by exactly one of the
// three lists above. Without this, a field added to the CRD in M6 would be
// owned by nobody: no writer sends it, so it can never be released, and the
// defect is invisible until someone notices the field is always empty --
// or, worse, a later task adds it to BOTH declarations and each apply starts
// deleting the other's value.
func TestEveryDownloadStatusFieldIsAccountedFor(t *testing.T) {
	typ := reflect.TypeOf(downloadv1alpha1.DownloadStatus{})

	claimed := map[string]int{}
	for _, list := range [][]string{controllerOwned, engineOwned, foreignOwned} {
		for _, name := range list {
			claimed[name]++
		}
	}

	for i := range typ.NumField() {
		name := typ.Field(i).Name
		assert.Equalf(t, 1, claimed[name],
			"DownloadStatus.%s is claimed by %d of {grabarr, grabarr-engine, importarr}; "+
				"it must be exactly one", name, claimed[name])
	}
	for name := range claimed {
		_, ok := typ.FieldByName(name)
		assert.Truef(t, ok, "%s is claimed by the split but is not a field of DownloadStatus", name)
	}
	require.Len(t, controllerOwned, 9)
	require.Len(t, engineOwned, 25)
	assert.Equal(t, typ.NumField(), len(controllerOwned)+len(engineOwned)+len(foreignOwned))
}

// ControllerFields must declare its whole set and nothing else. Conditions is
// the documented exception: the generated WithConditions appends, so seeding
// it would collide with the caller's own set.
func TestControllerFieldsDeclaresExactlyItsOwnSet(t *testing.T) {
	got := setFields(status.ControllerFields(fullStatus()))

	want := make([]string, 0, len(controllerOwned))
	for _, n := range controllerOwned {
		if n == "Conditions" {
			continue
		}
		want = append(want, n)
	}
	sort.Strings(want)

	assert.Equal(t, want, got)
}

// EngineFields must declare its whole set and nothing else -- in particular
// not status.import, which belongs to importarr, and not the controller's
// phase or failureReason even though the engine is what observes them.
func TestEngineFieldsDeclaresExactlyItsOwnSet(t *testing.T) {
	got := setFields(status.EngineFields(fullStatus()))

	want := append([]string(nil), engineOwned...)
	sort.Strings(want)

	assert.Equal(t, want, got)
}

// The two halves must not overlap. An overlap would not fail loudly at
// runtime: pkg/k8s.PatchStatus applies with ForceOwnership, so the apiserver
// transfers a contested field in silence and the losing manager's next apply
// simply takes it back.
func TestTheTwoDeclarationsAreDisjoint(t *testing.T) {
	st := fullStatus()
	engine := map[string]bool{}
	for _, n := range setFields(status.EngineFields(st)) {
		engine[n] = true
	}
	for _, n := range setFields(status.ControllerFields(st)) {
		assert.Falsef(t, engine[n], "%s is declared by BOTH grabarr and grabarr-engine", n)
	}
}
