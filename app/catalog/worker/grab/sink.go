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

package grab

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
)

// Config is the configuration governing a grab for one catalog item: which
// quality profile ranks it, which delay profile holds it back and which tags
// select that delay profile.
//
// An Episode has none of these on its own spec, so ResolveConfig reads them
// off the owning Series -- the same rule the Episode reconciler already
// applies when it resolves a quality profile.
type Config = grabContext

// ResolveConfig returns the quality profile ref, delay profile ref and tags
// governing ref. It is exported because the search worker's sink and the RSS
// matcher both need it and neither should restate the Episode-reads-its-Series
// rule.
func ResolveConfig(ctx context.Context, c client.Client, ns string, ref commonv1.MediaRef) (Config, error) {
	ops, err := kindOpsFor(statusKindOf(ref))
	if err != nil {
		return Config{}, err
	}
	name := ref.Name
	if ref.Kind == commonv1.MediaKindSeries {
		if len(ref.Keys) == 0 {
			return Config{}, fmt.Errorf("%w: a series target needs keys", ErrUnsupportedKind)
		}
		name = ref.Keys[0]
	}
	obj, err := ops.get(ctx, c, ns, name)
	if err != nil {
		return Config{}, err
	}
	return ops.grabContext(ctx, c, obj)
}

// statusKindOf maps a grab target onto the kind whose status carries the
// grab: a Series pack is governed through one of its Episodes.
func statusKindOf(ref commonv1.MediaRef) commonv1.MediaKind {
	if ref.Kind == commonv1.MediaKindSeries {
		return commonv1.MediaKindEpisode
	}
	return ref.Kind
}

// Sink turns the search worker's ranked, non-interactive results into a grab.
// It is the bridge between §8.2's two halves: the search worker decides, this
// package delays and grabs.
//
// It implements catalogarr/worker/search.Sink. That interface's Deliver takes
// the namespace the search worker resolved from the envelope, because neither
// a SearchTask nor a commonv1.MediaRef carries one while every object this
// package touches is namespaced, and the grab source the task's reason maps
// to.
type Sink struct {
	Deps Deps

	// ResolveProfile turns a quality profile name into a resolved profile.
	// It is a function rather than a client lookup so the caller decides
	// whether to cache; nil makes Deliver a no-op with a warning rather than
	// a panic.
	ResolveProfile func(ctx context.Context, name string) (quality.Profile, error)

	// ResolveDelay returns the DelayProfile spec governing an item.
	ResolveDelay func(ctx context.Context, ns string, ref *string, tags []string) (catalogv1alpha1.DelayProfileSpec, error)
}

// Deliver grabs the best approved release for target, honouring its delay
// profile. ranked is the search worker's output, best first; anything not
// approved is ignored, and an empty list is a successful no-op.
//
// grabbedBy is what the Download will record as spec.grabbedBy: the search
// worker passes redownload for a search a failed Download triggered (spec
// §8.3, catalogarr/worker/redownload) and search otherwise. Empty means
// search. It is carried through a delay as well (pendingValue.GrabbedBy), so
// a redownload held by a DelayProfile is still a redownload when the
// scheduled grab fires.
func (s Sink) Deliver(
	ctx context.Context,
	ns string,
	target commonv1.MediaRef,
	ranked []commonv1.ReleaseDecision,
	grabbedBy downloadv1alpha1.GrabSource,
) error {
	if grabbedBy == "" {
		grabbedBy = downloadv1alpha1.GrabSourceSearch
	}
	log := logging.FromContext(ctx).With("item", target.Name)
	best, ok := firstApproved(ranked)
	if !ok {
		return nil
	}
	if s.ResolveProfile == nil || s.ResolveDelay == nil {
		log.Warn("grab: sink is not fully wired; dropping an approved release")
		return nil
	}

	cfg, err := ResolveConfig(ctx, s.Deps.Client, ns, target)
	if err != nil {
		// Neither is worth a redelivery -- another search would get the
		// same answer -- but neither is silent: an approved release is
		// being dropped, and the log is the only place that says so.
		switch {
		case errors.Is(err, ErrUnsupportedKind):
			log.Warn("grab: dropping an approved release; the grab path cannot grab this kind",
				"kind", target.Kind, "release", best.Title, "error", err)
			return nil
		case apierrors.IsNotFound(err):
			log.Warn("grab: dropping an approved release; the item or the container it inherits its profiles from is gone",
				"kind", target.Kind, "release", best.Title, "error", err)
			return nil
		}
		return err
	}
	profile, err := s.ResolveProfile(ctx, cfg.QualityProfileRef)
	if err != nil {
		return err
	}
	delaySpec, err := s.ResolveDelay(ctx, ns, cfg.DelayProfileRef, cfg.Tags)
	if err != nil {
		return err
	}

	err = Decide(ctx, s.Deps, profile, delaySpec, Approved{
		Namespace: ns,
		Target:    commonv1.MediaRef{Kind: target.Kind, Name: target.Name},
		Keys:      target.Keys,
		Release:   best.ReleaseInfo,
		GrabbedBy: grabbedBy,
	})
	if errors.Is(err, ErrDuplicateGrab) {
		// Another path got there first. Acknowledge: the search is done
		// either way, and performGrab has already cleared
		// status.pendingGrab so the item does not strand at Delayed.
		log.Debug("grab: already grabbed elsewhere")
		return nil
	}
	return err
}

// firstApproved returns the best approved decision. The search worker already
// ranked the list best first, so this is a scan for the first Approved rather
// than a second ordering.
func firstApproved(ranked []commonv1.ReleaseDecision) (commonv1.ReleaseDecision, bool) {
	for _, d := range ranked {
		if d.Approved {
			return d, true
		}
	}
	return commonv1.ReleaseDecision{}, false
}
