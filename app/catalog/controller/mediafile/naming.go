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
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
)

// maxExpectedPathLen is NamingStatus.ExpectedPath's CRD MaxLength. A longer
// render is Unrenderable rather than sent: the apiserver would reject the
// whole status apply, and with it every other field this manager owns.
const maxExpectedPathLen = 4096

// The NamingCurrent condition's reasons when a path was rendered. When none
// was, the condition's reason is the NamingReason itself.
const (
	namingConditionCurrent = "Current"
	namingConditionStale   = "Stale"
)

// namingOwner is what renderNaming needs of the catalog item a MediaFile
// backs: the identity half of the naming context, the RootFolder the item
// lives under, and the renderer for its kind.
type namingOwner struct {
	context       naming.Context
	rootFolderRef string
	render        func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error)
}

// namingRetryAfter is how soon a reconcile whose render kept the previous
// proposal over a failed lookup is retried: the lookup is a cache read, so
// its failure is a blip, and nothing else would wake the reconcile.
const namingRetryAfter = 30 * time.Second

// namingInputs is what Reconcile knows about the file as of this apply that
// renderNaming cannot read off the object.
type namingInputs struct {
	// specPath is spec.path as of this apply: a swap incorporated in this
	// reconcile may have moved the file.
	specPath string
	// probeStale is true when the file's bytes changed since known's probe
	// and the probe that would describe them failed: known.MediaInfo then
	// describes bytes that are gone (ruling R20).
	probeStale bool
	// transcodePending is true when a TranscodeJob for the file is still
	// running, or finished without this apply incorporating it (ruling R18).
	transcodePending bool
}

// renderNaming proposes mf's canonical path for status.naming: the path an
// import of this file would have produced today, rendered through the same
// catalogctx calls fileimport makes (the item's metadata, the release-time
// spec, the probe, the RootFolder's naming config), so a rename lands the
// file exactly where a fresh import would have. importarr performs the
// rename; this only proposes.
//
// It reads the probe from known, not from mf.Status, because known is what
// this reconcile is about to apply: a probe taken earlier in the same
// reconcile is already in it. in says where the file is and what holds it.
//
// The result is nil for a kind this phase does not name (anything but a
// movie or an episode). Otherwise it carries a Reason and no ExpectedPath
// when the path cannot be rendered yet -- MetadataPending, TranscodePending
// (a transcode running or not yet incorporated: spec D6, a rename never
// touches a file another job holds), ProbePending (never probed, or the
// bytes changed and the re-probe failed), Unrenderable -- or the
// ExpectedPath and whether spec.path already is it. Recycling is never
// returned: this controller has no notion of a file in the recycle bin.
// Quality, the probe-corrected quality the rename re-applies into
// spec.quality, is set whenever a probe describes the file, even when it
// agreed with the name, so the rename has one value to apply.
//
// A failed lookup (anything but NotFound) keeps known.Naming, with Current
// re-judged against in.specPath, and reports retry: a cache blip must not
// replace a good proposal, returning nil would release status.naming
// (CLAUDE.md, the complete-declaration rule), and without a requeue nothing
// would render it again until the next unrelated event.
func (r *Reconciler) renderNaming(ctx context.Context, mf *catalogv1alpha1.MediaFile, known *knownStatus, in namingInputs) (naming *catalogv1alpha1.NamingStatus, retry bool) {
	log := logging.FromContext(ctx)
	owner, reason, err := r.namingOwner(ctx, mf)
	switch {
	case err != nil:
		log.Warn("mediafile: could not load the item to name the file; keeping the previous proposal", "error", err)
		return keepNaming(known.Naming, in.specPath), true
	case owner == nil && reason == "":
		return nil, false
	}

	mi := known.MediaInfo
	if in.probeStale {
		mi = nil
	}
	out := &catalogv1alpha1.NamingStatus{}
	spec := mf.Spec
	spec.Path = in.specPath
	if mi != nil {
		corrected, _ := quality.AugmentFromMediaInfo(spec.Quality, mi)
		spec.Quality = corrected
		out.Quality = &corrected
	}
	switch {
	case reason != "":
		out.Reason = reason
		return out, false
	case in.transcodePending:
		out.Reason = catalogv1alpha1.NamingReasonTranscodePending
		return out, false
	case mi == nil:
		out.Reason = catalogv1alpha1.NamingReasonProbePending
		return out, false
	}

	var root catalogv1alpha1.RootFolder
	if err := r.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: owner.rootFolderRef}, &root); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Warn("mediafile: could not load the RootFolder; keeping the previous proposal", "rootFolder", owner.rootFolderRef, "error", err)
			return keepNaming(known.Naming, in.specPath), true
		}
		log.Warn("mediafile: the item's RootFolder does not exist; the file cannot be named", "rootFolder", owner.rootFolderRef)
		out.Reason = catalogv1alpha1.NamingReasonUnrenderable
		return out, false
	}

	expected, err := owner.render(&root, catalogctx.File(owner.context, &spec, mi), catalogctx.ContainerExt(mi, spec.Path))
	if err == nil && len(expected) > maxExpectedPathLen {
		err = fmt.Errorf("the rendered path is %d bytes, over the %d status.naming.expectedPath holds", len(expected), maxExpectedPathLen)
	}
	if err != nil {
		log.Warn("mediafile: could not render the file's canonical path", "error", err)
		out.Reason = catalogv1alpha1.NamingReasonUnrenderable
		return out, false
	}
	out.ExpectedPath = expected
	out.Current = filepath.Clean(spec.Path) == expected
	return out, false
}

// keepNaming is prev, the proposal already on the object, re-judged
// against specPath: kept over a failed lookup, it must still say whether
// the file is at its proposed path now that a swap may have moved it.
func keepNaming(prev *catalogv1alpha1.NamingStatus, specPath string) *catalogv1alpha1.NamingStatus {
	if prev == nil {
		return nil
	}
	out := *prev
	if out.ExpectedPath != "" {
		out.Current = filepath.Clean(specPath) == out.ExpectedPath
	}
	return &out
}

// withNamingRetry shortens res's requeue to namingRetryAfter when a render
// kept its previous proposal over a failed lookup.
func withNamingRetry(res ctrl.Result, retry bool) ctrl.Result {
	if retry && (res.RequeueAfter == 0 || res.RequeueAfter > namingRetryAfter) {
		res.RequeueAfter = namingRetryAfter
	}
	return res
}

// namingOwner loads the item mf backs: a Movie, or every Episode the file
// covers (spec.mediaRef.name plus keys, a multi-episode file) and their
// Series. A missing item, or one whose metadata has not arrived, is
// MetadataPending; an episode reference naming no episode, or episodes of
// more than one series, is Unrenderable. It returns nil, "", nil for any
// other kind.
func (r *Reconciler) namingOwner(ctx context.Context, mf *catalogv1alpha1.MediaFile) (*namingOwner, catalogv1alpha1.NamingReason, error) {
	key := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: mf.Namespace, Name: name}
	}
	ref := mf.Spec.MediaRef
	switch ref.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := r.Get(ctx, key(ref.Name), &m); err != nil {
			return ownerNotLoaded(err)
		}
		c, ok := catalogctx.Movie(&m)
		if !ok {
			return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
		}
		return &namingOwner{
			context:       c,
			rootFolderRef: m.Spec.RootFolderRef,
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.MovieFilePath(root, &m, c, ext)
			},
		}, "", nil

	case commonv1.MediaKindEpisode:
		names := coveredEpisodeNames(ref)
		if len(names) == 0 {
			logging.FromContext(ctx).Warn("mediafile: the file's episode reference names no episode; it cannot be named")
			return nil, catalogv1alpha1.NamingReasonUnrenderable, nil
		}
		eps := make([]catalogv1alpha1.Episode, 0, len(names))
		for _, n := range names {
			var ep catalogv1alpha1.Episode
			if err := r.Get(ctx, key(n), &ep); err != nil {
				return ownerNotLoaded(err)
			}
			eps = append(eps, ep)
		}
		// fileimport renders from its matched episodes in this order.
		slices.SortFunc(eps, func(a, b catalogv1alpha1.Episode) int {
			if a.Spec.SeasonNumber != b.Spec.SeasonNumber {
				return int(a.Spec.SeasonNumber - b.Spec.SeasonNumber)
			}
			return int(a.Spec.EpisodeNumber - b.Spec.EpisodeNumber)
		})
		seriesRef := eps[0].Spec.SeriesRef
		for _, ep := range eps[1:] {
			if ep.Spec.SeriesRef != seriesRef {
				logging.FromContext(ctx).Warn("mediafile: the file covers episodes of more than one series; it cannot be named",
					"series", seriesRef, "otherSeries", ep.Spec.SeriesRef)
				return nil, catalogv1alpha1.NamingReasonUnrenderable, nil
			}
		}
		var s catalogv1alpha1.Series
		if err := r.Get(ctx, key(seriesRef), &s); err != nil {
			return ownerNotLoaded(err)
		}
		c, ok := catalogctx.Episode(&s, eps)
		if !ok {
			return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
		}
		return &namingOwner{
			context:       c,
			rootFolderRef: s.Spec.RootFolderRef,
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.EpisodeFilePath(root, &s, c, ext)
			},
		}, "", nil
	}
	return nil, "", nil
}

// ownerNotLoaded maps a failed Get of the item a file backs: an item that
// does not exist (yet) has no metadata to name the file from; anything else
// is an error the caller keeps its previous proposal over.
func ownerNotLoaded(err error) (*namingOwner, catalogv1alpha1.NamingReason, error) {
	if apierrors.IsNotFound(err) {
		return nil, catalogv1alpha1.NamingReasonMetadataPending, nil
	}
	return nil, "", err
}

// coveredEpisodeNames is every Episode an episode MediaRef covers, first
// the one it names, then keys (a multi-episode file lists every episode it
// holds there, its own name included -- fileimport.EpisodeFileRef),
// without duplicates or empty names.
func coveredEpisodeNames(ref commonv1.MediaRef) []string {
	out := make([]string, 0, 1+len(ref.Keys))
	for _, n := range append([]string{ref.Name}, ref.Keys...) {
		if n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// markNamingCurrent mirrors n onto the NamingCurrent condition, for kubectl
// and the UI: True/Current when spec.path is the proposal, False/Stale when
// it is not, and Unknown with the NamingReason as its reason when nothing
// was rendered to compare spec.path with. A nil n (a kind this phase does
// not name) sets nothing. applyStatus calls it, and nothing else sets this
// condition, so every apply carries exactly one NamingCurrent entry that
// agrees with the status.naming beside it.
func markNamingCurrent(obj *catalogv1alpha1.MediaFile, conditions *[]metav1.Condition, n *catalogv1alpha1.NamingStatus) {
	const cond = catalogv1alpha1.ConditionNamingCurrent
	switch {
	case n == nil:
	case n.Reason == catalogv1alpha1.NamingReasonMetadataPending:
		k8s.MarkUnknown(obj, conditions, cond, string(n.Reason), "the item's metadata has not arrived; the file cannot be named yet")
	case n.Reason == catalogv1alpha1.NamingReasonProbePending:
		k8s.MarkUnknown(obj, conditions, cond, string(n.Reason), "no probe describes the file's current bytes; it cannot be named yet")
	case n.Reason == catalogv1alpha1.NamingReasonTranscodePending:
		k8s.MarkUnknown(obj, conditions, cond, string(n.Reason), "a transcode of the file is running or has not been incorporated; the file is held")
	case n.Reason != "":
		k8s.MarkUnknown(obj, conditions, cond, string(n.Reason), "the file's canonical path could not be rendered; see the controller log")
	case n.Current:
		k8s.MarkTrue(obj, conditions, cond, namingConditionCurrent, "the file is at its canonical path")
	default:
		k8s.MarkFalse(obj, conditions, cond, namingConditionStale, "the file's canonical path is %s", n.ExpectedPath)
	}
}
