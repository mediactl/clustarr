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

package download

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// MaxStatusFiles is DownloadStatus.Files' +kubebuilder:validation:MaxItems;
// [ApplyStatus] never renders more.
const MaxStatusFiles = 200

// ApplyStatus renders item as the COMPLETE set of Download.status fields that
// k8s.ManagerGrabarrEngine owns -- every one of them, zero values included --
// and nothing outside that set.
//
// # Why "complete" and not "what changed"
//
// Server-side apply replaces a field manager's ownership set on every apply
// rather than merging into it. Any field the engine sent before and omits now
// is RELEASED, and a released field that nobody else owns is deleted from the
// object, which reads as "reset to zero". So every telemetry write is a whole
// declaration: downloadedBytes is sent when it is 0, canMoveFiles is sent when
// it is false, and seeders is sent for a usenet transfer that has none.
//
// The same rule applies one level down. status.health is a struct and
// server-side apply tracks ownership per LEAF inside it, so [healthAC] renders
// all four of its fields; a renderer that sent only healthPercent would
// release the other three on the next write. status.files is a listType=map
// keyed on path, where ownership is tracked per ENTRY: dropping a file from
// the Item removes that entry from status, which is right, but it also means
// an Item must always carry the complete file list.
//
// # Its relationship to app/grab/status.EngineFields
//
// They are the same declaration, not two. app/grab/status.EngineFields is
// literally ApplyStatus(ItemFromStatus(st)): the owned set is written down
// once, here, and the seed-from-live-status path reaches it through the
// inverse in [ItemFromStatus]. Two hand-built versions of one manager's write
// is exactly how each releases the other's fields, and this phase's
// predecessor shipped that bug three times.
//
// # What it deliberately omits
//
// Nine fields belong to k8s.ManagerGrabarr -- observedGeneration, phase,
// engine, failureReason, blocklistedUntil, startedAt, completedAt,
// seedGoalMetAt and conditions -- and one, import, belongs to
// k8s.ManagerImportarr. None of them is emitted here. [Item.FailureReason]
// and [Item.SeedGoalMet] are the raw material for two of them, and are
// emitted as the engine's own reports (engineFailureReason,
// seedGoalReached), which the controller reads and turns into its verdicts;
// [Item.HealthPaused] is likewise the report (healthPaused) behind the
// controller's Paused phase.
//
// Three pointer fields are omitted when nil, and that is a property of the
// SHAPE of the observation rather than of its outcome: etaSeconds is absent
// because the engine has no estimate to give, health is absent because a
// torrent has no articles, and lastProgressAt is absent because nothing has
// been downloaded yet. In each case "absent" is the value the CRD documents,
// so releasing the field is what a caller wants. Contrast an outcome-driven
// omission -- skipping health because this poll failed -- which would delete a
// good value; [Item] has no way to express that, on purpose.
func ApplyStatus(item Item) *downloadac.DownloadStatusApplyConfiguration {
	ac := downloadac.DownloadStatus().
		WithDownloadID(item.ID).
		WithOutputPath(item.OutputPath).
		WithContentRoot(item.ContentRoot).
		WithTotalBytes(item.TotalBytes).
		WithRemainingBytes(item.RemainingBytes).
		WithDownloadedBytes(item.DownloadedBytes).
		WithUploadedBytes(item.UploadedBytes).
		WithDownloadRateBps(item.DownRate).
		WithUploadRateBps(item.UpRate).
		WithProgressPercent(clampPercent(item.ProgressPercent)).
		WithSeeders(clampNonNegative32(int64(item.Seeders))).
		WithPeers(clampNonNegative32(int64(item.Peers))).
		WithRatioMilli(clampNonNegative32(int64(item.RatioMilli))).
		WithSeedTimeSeconds(int64(item.SeedTime / time.Second)).
		WithIsEncrypted(item.IsEncrypted).
		WithCanMoveFiles(item.CanMoveFiles).
		WithCanBeRemoved(item.CanBeRemoved).
		WithSeedGoalReached(item.SeedGoalMet).
		WithHealthPaused(item.HealthPaused).
		WithMessage(item.Message)

	// Stage is an enum whose generated CRD does not admit "". An engine that
	// has not decided on a stage yet omits the field instead of sending a
	// value the apiserver rejects -- which would fail the whole telemetry
	// write, not just this leaf. It is the same shape-not-outcome exception
	// app/indexer/status makes for Indexer.status.protocol.
	if item.Stage != "" {
		ac = ac.WithStage(item.Stage)
	}
	// engineFailureReason is absent while the transfer has not failed: a
	// shape, like stage above, not an outcome. A client reports "not failed"
	// as either "" or DownloadFailureNone (usenet starts every job at none),
	// and sending none would read as a failure named "none".
	if item.FailureReason.IsFailure() {
		ac = ac.WithEngineFailureReason(item.FailureReason)
	}
	if item.ETA != nil {
		ac = ac.WithETASeconds(clampNonNegative32(int64(*item.ETA / time.Second)))
	}
	if item.Health != nil {
		ac = ac.WithHealth(healthAC(item.Health))
	}
	if item.LastProgressAt != nil {
		ac = ac.WithLastProgressAt(metav1.NewTime(*item.LastProgressAt))
	}
	// WithFiles APPENDS. That is safe here and only here, because ac is
	// freshly constructed above: the file list is declared exactly once per
	// apply configuration. A caller that seeds from [ItemFromStatus] and then
	// calls WithFiles again gets duplicate entries, which is why
	// app/grab/status.Patch tells its callers to assign ac.Files instead.
	//
	// The list is capped at [MaxStatusFiles], the CRD's MaxItems: a
	// discography or a full-series pack easily lists more, and the apiserver
	// rejects the WHOLE apply, not the excess -- phase, progress and every
	// other engine field would freeze with it. Nothing reads status.files
	// to decide anything (the importer walks the content root), so the
	// first MaxStatusFiles entries are an honest sample, not a loss.
	files := item.Files
	if len(files) > MaxStatusFiles {
		files = files[:MaxStatusFiles]
	}
	for _, f := range files {
		ac = ac.WithFiles(downloadac.DownloadFile().
			WithPath(f.Path).
			WithSizeBytes(f.SizeBytes).
			WithSkipped(f.Skipped))
	}
	return ac
}

// ItemFromStatus is the inverse of [ApplyStatus] over the engine-owned fields:
// it reads a live Download.status back into the Item that would reproduce it.
//
// It exists so that app/grab/status.EngineFields can seed a complete
// declaration from the object without a second copy of the field list. It
// reads ONLY engine-owned fields; st.Phase, st.Import and the rest are not
// representable on an [Item] at all, so the inverse cannot accidentally widen
// the engine's claim.
//
// The round trip is exact on the status side -- ApplyStatus(ItemFromStatus(st))
// reproduces every engine-owned leaf of st -- which is the only direction that
// matters for a seed, whose whole job is to re-declare what the object already
// holds. It is NOT exact in the other direction: Item.Status has no status
// field to come back from, so a caller must not use this to recover an
// engine's own view of a transfer. Ask the client.
func ItemFromStatus(st downloadv1alpha1.DownloadStatus) Item {
	item := Item{
		ID:              st.DownloadID,
		Stage:           st.Stage,
		TotalBytes:      st.TotalBytes,
		RemainingBytes:  st.RemainingBytes,
		DownloadedBytes: st.DownloadedBytes,
		UploadedBytes:   st.UploadedBytes,
		DownRate:        st.DownloadRateBps,
		UpRate:          st.UploadRateBps,
		ProgressPercent: st.ProgressPercent,
		RatioMilli:      st.RatioMilli,
		SeedTime:        time.Duration(st.SeedTimeSeconds) * time.Second,
		Seeders:         int(st.Seeders),
		Peers:           int(st.Peers),
		OutputPath:      st.OutputPath,
		ContentRoot:     st.ContentRoot,
		CanMoveFiles:    st.CanMoveFiles,
		CanBeRemoved:    st.CanBeRemoved,
		IsEncrypted:     st.IsEncrypted,
		SeedGoalMet:     st.SeedGoalReached,
		HealthPaused:    st.HealthPaused,
		FailureReason:   st.EngineFailureReason,
		Message:         st.Message,
	}
	if st.EngineFailureReason.IsFailure() {
		// The only status a failure reason is ever reported with. Nothing
		// renders Status, so this is for a caller's benefit, not the round
		// trip's.
		item.Status = StatusFailed
	}
	if st.ETASeconds != nil {
		eta := time.Duration(*st.ETASeconds) * time.Second
		item.ETA = &eta
	}
	if st.Health != nil {
		item.Health = st.Health.DeepCopy()
	}
	if st.LastProgressAt != nil {
		t := st.LastProgressAt.Time
		item.LastProgressAt = &t
	}
	for _, f := range st.Files {
		item.Files = append(item.Files, File{Path: f.Path, SizeBytes: f.SizeBytes, Skipped: f.Skipped})
	}
	return item
}

// healthAC renders every field of h, zero values included, for the per-leaf
// reason [ApplyStatus] gives.
func healthAC(h *downloadv1alpha1.UsenetHealth) *downloadac.UsenetHealthApplyConfiguration {
	return downloadac.UsenetHealth().
		WithHealthPercent(clampPercent(h.HealthPercent)).
		WithCriticalHealthPercent(clampPercent(h.CriticalHealthPercent)).
		WithFailedArticles(clampNonNegative32(int64(h.FailedArticles))).
		WithTotalArticles(clampNonNegative32(int64(h.TotalArticles)))
}

// clampPercent holds v inside the CRD's 0-100 range.
//
// Clamping rather than passing the value through is deliberate. These are
// derived percentages, and an engine that computed 101 from a transfer whose
// wanted size shrank would otherwise fail EVERY telemetry apply from that
// point on with a validation error -- one arithmetic slip taking the whole
// download's observability down with it. The apiserver enforces the same
// bound, so clamping cannot hide a value that would otherwise have been
// stored.
func clampPercent(v int32) int32 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	default:
		return v
	}
}

// clampNonNegative32 holds v at or above zero and inside int32, for the CRD
// fields marked Minimum=0. The int64 parameter is what makes the int-to-int32
// narrowing safe: an engine reporting an absurd peer count is clamped rather
// than wrapped to a negative one.
func clampNonNegative32(v int64) int32 {
	switch {
	case v < 0:
		return 0
	case v > int64(^uint32(0)>>1):
		return int32(^uint32(0) >> 1)
	default:
		return int32(v)
	}
}
