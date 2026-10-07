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

package actions

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// maxGrabs is Search.spec.grab's MaxItems.
const maxGrabs = 50

// InteractiveSearch is Sonarr's and Radarr's interactive search: it creates
// a Search for the catalog item kind/name in namespace exactly as
// [SearchNow] does, but without grabBest, so the Search only answers its
// ranked results in status.results. A person then picks from them with
// [GrabRelease].
func InteractiveSearch(
	ctx context.Context, c Creator, namespace string, kind commonv1.MediaKind, name string,
) (*catalogv1alpha1.Search, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.InteractiveSearch")
	defer span.End()

	if err := validateItem(namespace, kind, name); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	search := uiSearch(namespace, kind, name)
	if err := c.Create(ctx, search, client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: create interactive Search for %s %s/%s: %w", kind, namespace, name, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: interactive search requested",
		"search", search.Name, "namespace", namespace, "kind", kind, "item", name)
	return search, nil
}

// SearchSeason is a season row's magnifier (Wave S, Sonarr's season
// search): a Search with grabBest and spec.season, which the Series plans
// -- one paced query for the season, the best release for each wanted
// episode grabbed, packs preferred on a tie.
func SearchSeason(ctx context.Context, c Creator, namespace, series string, season int32) (*catalogv1alpha1.Search, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.SearchSeason")
	defer span.End()

	search, err := seasonSearch(namespace, series, season)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	search.Spec.GrabBest = true
	if err := c.Create(ctx, search, client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: create season Search for series %s/%s season %d: %w", namespace, series, season, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: season search requested",
		"search", search.Name, "namespace", namespace, "series", series, "season", season)
	return search, nil
}

// InteractiveSeasonSearch is a season row's person (Wave S, Sonarr's
// interactive season search): a Search with spec.season and no grabBest,
// its ranked results -- packs and singles judged against the season -- in
// status.results for a person to pick from with [GrabRelease].
func InteractiveSeasonSearch(ctx context.Context, c Creator, namespace, series string, season int32) (*catalogv1alpha1.Search, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.InteractiveSeasonSearch")
	defer span.End()

	search, err := seasonSearch(namespace, series, season)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	if err := c.Create(ctx, search, client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: create interactive season Search for series %s/%s season %d: %w", namespace, series, season, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: interactive season search requested",
		"search", search.Name, "namespace", namespace, "series", series, "season", season)
	return search, nil
}

// seasonSearch is the Search a season's two buttons create: [uiSearch] on
// the series with spec.season set. A negative season is [ErrInvalid].
func seasonSearch(namespace, series string, season int32) (*catalogv1alpha1.Search, error) {
	if err := validateItem(namespace, commonv1.MediaKindSeries, series); err != nil {
		return nil, err
	}
	if season < 0 {
		return nil, fmt.Errorf("%w: season %d is not a season", ErrInvalid, season)
	}
	search := uiSearch(namespace, commonv1.MediaKindSeries, series)
	n := season
	search.Spec.Season = &n
	return search, nil
}

// GrabRelease is the interactive search's download button: it adds guid to
// the spec.grab of search -- the Search as the caller read it -- and the
// Search controller sends the pick to the item's owner as a manual
// Candidate (grabbedBy interactive), whose grab planner makes the grab
// entry status.grabbed then names (ADR-0019 §6.6). override
// sets spec.override, which a release with a permanent rejection needs;
// the caller decides, since it shows the person the rejection first.
//
// spec.grab is a list, which a JSON merge patch replaces whole, so the
// patch sends the list read plus guid, and the read resourceVersion: a
// grab made meanwhile is a Conflict, never silently dropped. A guid
// already requested is not sent again; the Search is returned as given.
func GrabRelease(
	ctx context.Context, p Patcher, search *catalogv1alpha1.Search, guid string, override bool,
) (*catalogv1alpha1.Search, error) {
	ctx, span := tracing.Start(ctx, "ui.actions.GrabRelease")
	defer span.End()

	if err := validateGrab(search, guid); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	requested := slices.Contains(search.Spec.Grab, guid)
	if requested && (!override || search.Spec.Override) {
		return search, nil
	}

	var body grabPatch
	body.Metadata.ResourceVersion = search.ResourceVersion
	body.Spec.Grab = search.Spec.Grab
	if !requested {
		body.Spec.Grab = append(slices.Clone(search.Spec.Grab), guid)
	}
	if override {
		body.Spec.Override = new(true)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		err = fmt.Errorf("actions: encode spec.grab patch: %w", err)
		tracing.RecordError(span, err)
		return nil, err
	}

	patched := &catalogv1alpha1.Search{}
	patched.Namespace, patched.Name = search.Namespace, search.Name
	if err := p.Patch(ctx, patched, client.RawPatch(types.MergePatchType, raw), client.FieldOwner(FieldManager)); err != nil {
		err = fmt.Errorf("actions: grab %s from Search %s/%s: %w", guid, search.Namespace, search.Name, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	logging.FromContext(ctx).Info("ui action: release grab requested",
		"search", search.Name, "namespace", search.Namespace, "override", override)
	return patched, nil
}

// grabPatch is GrabRelease's merge patch: the resourceVersion read, so a
// concurrent grab conflicts, and the whole spec.grab list.
type grabPatch struct {
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
	} `json:"metadata"`
	Spec struct {
		Grab     []string `json:"grab"`
		Override *bool    `json:"override,omitempty"`
	} `json:"spec"`
}

// validateGrab is GrabRelease's precondition: a Search read from the
// cluster (named, with a resourceVersion), a non-empty guid, and room in
// spec.grab.
func validateGrab(search *catalogv1alpha1.Search, guid string) error {
	switch {
	case search == nil || search.Namespace == "" || search.Name == "":
		return fmt.Errorf("%w: need the Search to grab from", ErrInvalid)
	case search.ResourceVersion == "":
		return fmt.Errorf("%w: Search %s/%s was not read from the cluster", ErrInvalid, search.Namespace, search.Name)
	case guid == "":
		return fmt.Errorf("%w: need the release's guid", ErrInvalid)
	case !slices.Contains(search.Spec.Grab, guid) && len(search.Spec.Grab) >= maxGrabs:
		return fmt.Errorf("%w: Search %s/%s already holds %d grabs, the most it takes", ErrInvalid, search.Namespace, search.Name, maxGrabs)
	}
	return nil
}

// uiSearch is the Search [SearchNow] and [InteractiveSearch] create: named
// "<name>-<random>", labelled [LabelOrigin]=[OriginUI], every other spec
// field left to its CRD default.
func uiSearch(namespace string, kind commonv1.MediaKind, name string) *catalogv1alpha1.Search {
	search := &catalogv1alpha1.Search{
		Spec: catalogv1alpha1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: kind, Name: name}},
	}
	search.GenerateName = name + "-"
	search.Namespace = namespace
	search.Labels = map[string]string{LabelOrigin: OriginUI}
	return search
}

// InteractiveSearch is [InteractiveSearch] over the Actions' writer.
func (a *Actions) InteractiveSearch(
	ctx context.Context, namespace string, kind commonv1.MediaKind, name string,
) (*catalogv1alpha1.Search, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return InteractiveSearch(ctx, a.w, namespace, kind, name)
}

// SearchSeason is [SearchSeason] over the Actions' writer.
func (a *Actions) SearchSeason(ctx context.Context, namespace, series string, season int32) (*catalogv1alpha1.Search, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return SearchSeason(ctx, a.w, namespace, series, season)
}

// InteractiveSeasonSearch is [InteractiveSeasonSearch] over the Actions'
// writer.
func (a *Actions) InteractiveSeasonSearch(ctx context.Context, namespace, series string, season int32) (*catalogv1alpha1.Search, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return InteractiveSeasonSearch(ctx, a.w, namespace, series, season)
}

// GrabRelease is [GrabRelease] over the Actions' writer.
func (a *Actions) GrabRelease(
	ctx context.Context, search *catalogv1alpha1.Search, guid string, override bool,
) (*catalogv1alpha1.Search, error) {
	if a == nil || a.w == nil {
		return nil, ErrNoWriter
	}
	return GrabRelease(ctx, a.w, search, guid, override)
}
