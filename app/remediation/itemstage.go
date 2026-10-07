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

package remediation

import (
	"context"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"time"

	"go.opentelemetry.io/otel/codes"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ItemStageName names one item stage (ADR-0019 §7.0, ruling R5).
type ItemStageName string

// The item stages.
const (
	StageSearch    ItemStageName = "search"
	StageGrab      ItemStageName = "grab"
	StageDownloads ItemStageName = "downloads"
	StageMetadata  ItemStageName = "metadata"
	StageArtwork   ItemStageName = "artwork"
	StageOverlay   ItemStageName = "overlay"
)

// StageOrder: grab decides new entries from candidates, downloads runs the
// state machine over stored and new entries, search reads the downloads
// outcome (redownloads, R25), then metadata, artwork and overlay.
var StageOrder = []ItemStageName{StageGrab, StageDownloads, StageSearch, StageMetadata, StageArtwork, StageOverlay}

// setOrder is the order the other managers' sets apply in, each
// preconditioned on the resourceVersion the one before returned (§7.0).
var setOrder = []k8s.FieldManager{k8s.ManagerCatalogGrab, k8s.ManagerCatalogMetadata, k8s.ManagerCatalogArtwork}

// ItemView is what one item pass's stages read.
type ItemView struct {
	Key Key
	// Item is the cached item, deep-copied; nil when the owner is gone
	// (convergence only).
	Item client.Object
	// Now is the pass's clock, UTC, whole seconds.
	Now metav1.Time
	// NewEntries are the grab stage's decisions this pass.
	NewEntries []catalogv1alpha1.DownloadEntry
	// Downloads is the downloads stage's outcome, for search.
	Downloads *DownloadsOutcome
}

// DownloadsOutcome is the downloads stage's outcome a later stage reads.
type DownloadsOutcome struct {
	Entries []catalogv1alpha1.DownloadEntry
	// Redownloads are the items a Failed or Blocklisted entry asks to search
	// (R25).
	Redownloads []schema.ItemRef
}

// ItemResult is what one stage decided.
type ItemResult struct {
	// Contribution is the stage's part of the catalogarr apply; each field
	// comes from at most one stage.
	Contribution itempass.Contribution
	// Sets are the stage's parts of other managers' sets; two stages naming
	// one manager are merged by field, each field from at most one stage.
	Sets       []itempass.Set
	NewEntries []catalogv1alpha1.DownloadEntry
	Downloads  *DownloadsOutcome
	// Effects run after every apply landed, in the stage's order.
	Effects []Effect
	Events  []ItemEvent
	// Due is when the stage next needs a pass; zero is no timer.
	Due time.Time
	// Again asks for a pass in 1 s.
	Again bool
}

// ItemEvent is one Kubernetes Event a stage owes. Recorder names the
// recorder ("" is the item kind's); On nil is the item.
type ItemEvent struct {
	Recorder, Type, Reason, Action, Message string
	On                                      client.Object
}

// ItemStage is one planner of an item key beside the kind's rollups
// (ruling R5). Plan reads the cache, records and leader-local books and
// decides; it writes nothing.
type ItemStage interface {
	Name() ItemStageName
	Applies(kind KeyKind) bool
	Plan(ctx context.Context, env *Env, v *ItemView) (ItemResult, error)
}

// OwnerGone is implemented by the downloads stage: a key NotFound in the
// cache that the unclaimed index holds is decided on the APIReader's answer
// (§6.7).
type OwnerGone interface {
	Holds(k Key) bool
	Gone(ctx context.Context, env *Env, k Key) ([]Effect, []ItemEvent, error)
}

// stageOutcome is one stage's isolated run.
type stageOutcome struct {
	res    ItemResult
	failed outcome
}

// runStage runs one stage's Plan under recover, spanned and timed as a
// planner is (§3.6, §3.19): a panic or error marks the stage failed for this
// pass, and its contribution, sets and effects are dropped, so the kind
// re-sends the stored values.
func runStage(ctx context.Context, env *Env, s ItemStage, v *ItemView) (so stageOutcome) {
	name := "item." + string(s.Name())
	start := time.Now()
	ctx, span := tracing.Start(ctx, "remediation."+name+".plan")
	defer func() {
		if p := recover(); p != nil {
			logging.FromContext(ctx).Error("remediation: item stage panicked", "stage", s.Name(), "panic", p, "stack", string(debug.Stack()))
			so = stageOutcome{failed: outcome{reason: failPanic, message: fmt.Sprintf("%s panicked: %v", s.Name(), p)}}
		}
		metrics.RemediationPlannerSeconds.WithLabelValues(name, "plan").Observe(time.Since(start).Seconds())
		if so.failed.failed() {
			metrics.RemediationPlannerFailuresTotal.WithLabelValues(name, so.failed.reason).Inc()
			span.SetStatus(codes.Error, so.failed.reason+": "+so.failed.message)
		}
		span.End()
	}()
	res, err := s.Plan(ctx, env, v)
	switch {
	case err == nil:
		return stageOutcome{res: res}
	case IsTransient(err):
		return stageOutcome{failed: outcome{reason: failTransient, message: err.Error()}}
	default:
		return stageOutcome{failed: outcome{reason: failError, message: err.Error()}}
	}
}

// mergeContribution folds c into into, each field from at most one stage;
// a second stage setting a field is a programming error.
func mergeContribution(into *itempass.Contribution, c itempass.Contribution, stage ItemStageName) error {
	dup := func(field string) error {
		return fmt.Errorf("remediation: stage %s sets %s, which another stage set this pass", stage, field)
	}
	if c.Downloads != nil {
		if into.Downloads != nil {
			return dup("downloads")
		}
		into.Downloads = c.Downloads
	}
	if c.DownloadPhase != nil {
		if into.DownloadPhase != nil {
			return dup("downloadPhase")
		}
		into.DownloadPhase = c.DownloadPhase
	}
	if c.DownloadNonces != nil {
		if into.DownloadNonces != nil {
			return dup("downloadNonces")
		}
		into.DownloadNonces = c.DownloadNonces
	}
	if c.LegacyDownloads != nil {
		if into.LegacyDownloads != nil {
			return dup("legacyDownloads")
		}
		into.LegacyDownloads = c.LegacyDownloads
	}
	if c.Viewed {
		if into.Viewed {
			return dup("the episode or issue view")
		}
		into.Viewed, into.Covering, into.ActiveDownloadRef, into.DonorOpen = true, c.Covering, c.ActiveDownloadRef, c.DonorOpen
	}
	return nil
}

// mergeSets folds each stage's sets into one per manager, each field from
// at most one stage.
func mergeSets(into map[k8s.FieldManager]itempass.Set, sets []itempass.Set, stage ItemStageName) error {
	for _, s := range sets {
		cur, ok := into[s.Manager]
		if !ok {
			cur = itempass.Set{Manager: s.Manager, Fields: map[string]any{}}
		}
		for name, v := range s.Fields {
			if _, dup := cur.Fields[name]; dup {
				return fmt.Errorf("remediation: stage %s sets %s under %s, which another stage set this pass", stage, name, s.Manager)
			}
			cur.Fields[name] = v
		}
		into[s.Manager] = cur
	}
	return nil
}

// orderedSets is merged's sets in setOrder, then any other manager by name.
func orderedSets(merged map[k8s.FieldManager]itempass.Set) []itempass.Set {
	var out []itempass.Set
	for _, m := range setOrder {
		if s, ok := merged[m]; ok {
			out = append(out, s)
		}
	}
	for _, m := range slices.Sorted(maps.Keys(merged)) {
		if !slices.Contains(setOrder, m) {
			out = append(out, merged[m])
		}
	}
	return out
}
