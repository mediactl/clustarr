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

package rssmatcher

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/delayprofile"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/catalogarr/worker/grab"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
)

// defaultIndexerPriority is the priority the decision engine assumes for an
// indexer it cannot read. It matches docs/research/indexers.md's documented
// default, which pkg/decision applies for a missing key anyway; restating it
// keeps the ranking stable when the Indexer object is momentarily unreadable.
const defaultIndexerPriority = 25

// resolveState is the per-item state Handle assembles for the decision engine.
// It is a struct so the "what does an item look like to pkg/decision" logic
// stays in one readable place instead of being threaded through six
// parameters.
type resolveState struct {
	monitored      bool
	available      bool
	runtimeMinutes int
	// originalLanguageTag is the BCP-47 tag verbatim from
	// status.metadata.originalLanguage, which is what
	// decision.Target.OriginalLanguageTag wants. Never a display name.
	originalLanguageTag string
	qualityProfile      string
	delayProfileRef     *string
	tags                []string
	currentFile         *decision.Current
	// identity is built by catalogarr/worker/search's own MovieIdentity and
	// EpisodeIdentity, so an RSS decision and a search decision agree on
	// what the item is. The matcher already picked the item by id or by
	// title and year; the decision engine checks the release against it
	// anyway, because a title-and-year match is exactly the kind of guess
	// that check exists to confirm -- and a release whose own id contradicts
	// the item it was title-matched to must not be grabbed for it.
	identity decision.Identity
	// episodes are the Episode objects a pack target covers (ref.Keys), for
	// narrowing the grab to the ones that want the release (wantedKeys).
	// Empty for a movie or a single episode.
	episodes []*catalogv1alpha1.Episode
	// finalEpisodes names the episodes of a pack whose file is transcoded
	// (rollup.Transcoded). A transcoded file is final, so wantedKeys never
	// hands the grab one of them, however much better the pack is. A
	// single target carries the same verdict on currentFile.Transcoded.
	finalEpisodes map[string]bool
}

// resolve fetches the item named by ref and reads off everything the decision
// engine and the delay profile need.
//
// It is deliberately narrower than the search worker's own snapshot: the
// current file is read from the item's rolled-up status
// (hasFile/fileQuality/fileFormatScore) rather than from the MediaFile, and
// the already-imported source hash and title are not resolved. The one fact
// read from the MediaFile itself is whether it is transcoded, because
// rollup.Transcoded is the one place that rule lives and it reads the file:
// a transcoded file is final, and the RSS path is the likeliest automatic
// grab of all.
// (reconciled by controller -- Task C12): once
// catalogarr/worker/search exports its snapshot builder, this should call it
// instead, so an RSS decision and a search decision see byte-identical input.
// The reduction is safe in one direction only -- it can approve an upgrade a
// fuller snapshot would have rejected as already-imported, and the grab's own
// lease plus its lookup of the item's live Downloads still stop a duplicate
// Download.
func resolve(ctx context.Context, c client.Client, ns string, ref commonv1.MediaRef, now time.Time, scenes sceneLookup) (resolveState, error) {
	var st resolveState
	switch ref.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &m); err != nil {
			return st, err
		}
		st.monitored = ptr.Deref(m.Spec.Monitored, true)
		st.available = m.Status.Available
		st.qualityProfile = m.Spec.QualityProfileRef
		st.delayProfileRef = m.Spec.DelayProfileRef
		st.tags = m.Spec.Tags
		if m.Status.Metadata != nil {
			st.runtimeMinutes = int(m.Status.Metadata.RuntimeMinutes)
			st.originalLanguageTag = m.Status.Metadata.OriginalLanguage
		}
		st.currentFile = currentFrom(m.Status.HasFile, m.Status.FileQuality, m.Status.FileFormatScore)
		if err := markTranscoded(ctx, c, ns, st.currentFile, m.Status.FileRef); err != nil {
			return st, err
		}
		st.identity = search.MovieIdentity(&m)

	case commonv1.MediaKindEpisode, commonv1.MediaKindSeries:
		seriesName := ref.Name
		var eps []*catalogv1alpha1.Episode
		if ref.Kind == commonv1.MediaKindEpisode {
			var ep catalogv1alpha1.Episode
			if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &ep); err != nil {
				return st, err
			}
			seriesName = ep.Spec.SeriesRef
			st.monitored = ptr.Deref(ep.Spec.Monitored, true)
			st.available = ep.Status.AirDate != nil && !now.Before(ep.Status.AirDate.Time)
			st.currentFile = currentFrom(ep.Status.HasFile, ep.Status.FileQuality, ep.Status.FileFormatScore)
			if err := markTranscoded(ctx, c, ns, st.currentFile, ep.Status.FileRef); err != nil {
				return st, err
			}
			eps = append(eps, &ep)
		} else {
			// A pack has no single file or air date of its own. It is
			// available because its episodes were matched, and its current
			// file is per-episode -- the importer re-checks each one.
			st.monitored = true
			st.available = true
			// Its numbering, though, is exactly the episodes the matcher
			// resolved it to (packRef's Keys), and the decision engine's
			// identity check needs them: without them a pack target has no
			// numbering and every pack release fails closed as UnknownItem.
			for _, name := range ref.Keys {
				var ep catalogv1alpha1.Episode
				if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &ep); err != nil {
					return st, err
				}
				eps = append(eps, &ep)
				if !ep.Status.HasFile {
					continue
				}
				final, err := transcodedFile(ctx, c, ns, ep.Status.FileRef)
				if err != nil {
					return st, err
				}
				if final {
					if st.finalEpisodes == nil {
						st.finalEpisodes = map[string]bool{}
					}
					st.finalEpisodes[ep.Name] = true
				}
			}
			st.episodes = eps
		}
		var s catalogv1alpha1.Series
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: seriesName}, &s); err != nil {
			return st, fmt.Errorf("rssmatcher: get series %q: %w", seriesName, err)
		}
		st.identity = search.EpisodeIdentity(&s, eps...)
		// The series' whole scene table, read by the same code as the
		// search worker's (search.SceneMappings), so the two paths read a
		// scene number the same way. SingleEpisodeSearch stays false: an
		// RSS release was not asked for anything, and a pack that covers
		// the episodes it was matched to is exactly what it may grab.
		st.identity.SceneMappings = scenes.table(ctx, s.Spec.TvdbID)
		st.qualityProfile = s.Spec.QualityProfileRef
		st.delayProfileRef = s.Spec.DelayProfileRef
		st.tags = s.Spec.Tags
		st.monitored = st.monitored && ptr.Deref(s.Spec.Monitored, true)
		if s.Status.Metadata != nil {
			st.runtimeMinutes = int(s.Status.Metadata.RuntimeMinutes)
			st.originalLanguageTag = s.Status.Metadata.OriginalLanguage
		}

	default:
		return st, fmt.Errorf("%w: %s", grab.ErrUnsupportedKind, ref.Kind)
	}
	return st, nil
}

func currentFrom(hasFile bool, q *commonv1.Quality, formatScore int32) *decision.Current {
	if !hasFile || q == nil {
		return nil
	}
	return &decision.Current{Quality: *q, FormatScore: int(formatScore)}
}

// markTranscoded sets cur.Transcoded from the MediaFile fileRef names, so the
// decision engine rejects an automatic grab over a transcoded file
// (decision.ReasonTranscodedFinal). A nil cur -- no file -- has nothing to
// mark.
func markTranscoded(ctx context.Context, c client.Client, ns string, cur *decision.Current, fileRef *string) error {
	if cur == nil {
		return nil
	}
	final, err := transcodedFile(ctx, c, ns, fileRef)
	if err != nil {
		return err
	}
	cur.Transcoded = final
	return nil
}

// transcodedFile reports whether the MediaFile fileRef names is transcoded
// (rollup.Transcoded). A MediaFile that is gone is not: the item's rollup has
// not caught up with a deletion yet, and deciding as if there were no
// transcoded file can only approve an upgrade of a file that no longer
// exists -- the reading the search worker's currentFile takes. Any other
// read failure is returned, so the release is retried rather than decided
// against a guess. The error never wraps NotFound, which decideOne reads as
// "the item itself is gone".
func transcodedFile(ctx context.Context, c client.Client, ns string, fileRef *string) (bool, error) {
	if fileRef == nil || *fileRef == "" {
		return false, nil
	}
	var mf catalogv1alpha1.MediaFile
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: *fileRef}, &mf); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("rssmatcher: get MediaFile %q: %w", *fileRef, err)
	}
	return rollup.Transcoded(&mf), nil
}

// queueFor lists the Downloads already working on ref, through the search
// worker's exported IndexDownloadTarget index. A read failure is a warning,
// not an error: an empty queue can only approve a release the fuller check
// might have deferred, and the grab's lease plus its lookup of the item's
// live Downloads still stop a second Download for the same item.
func queueFor(ctx context.Context, c client.Client, ns string, ref commonv1.MediaRef) []decision.Queued {
	var list downloadv1alpha1.DownloadList
	if err := c.List(ctx, &list,
		client.InNamespace(ns),
		client.MatchingFields{search.IndexDownloadTarget: search.TargetIndexValue(ref)},
	); err != nil {
		logging.FromContext(ctx).Warn("rssmatcher: queue lookup failed; deciding against an empty queue", "error", err)
		return nil
	}
	out := make([]decision.Queued, 0, len(list.Items))
	for i := range list.Items {
		r := list.Items[i].Spec.Release
		out = append(out, quality.Candidate{Quality: r.Quality, Revision: r.Revision, FormatScore: int(r.FormatScore)})
	}
	return out
}

// decisionOptions folds the delay profile's protocol switches and the
// indexer's priority into pkg/decision's Options.
//
// ProtocolsEnabled is populated for BOTH protocols on every call because
// pkg/decision fails closed on a missing key: leaving one out would silently
// reject every release of that protocol.
func decisionOptions(ctx context.Context, c client.Client, ns string, dp catalogv1alpha1.DelayProfileSpec, rel schema.Release) decision.Options {
	o := decision.Options{
		ProtocolsEnabled: map[string]bool{
			string(commonv1.ProtocolTorrent): grab.ProtocolEnabled(dp, commonv1.ProtocolTorrent),
			string(commonv1.ProtocolUsenet):  grab.ProtocolEnabled(dp, commonv1.ProtocolUsenet),
		},
		IndexerPriority:   map[string]int{},
		PreferredProtocol: string(dp.PreferredProtocol),
	}
	if name := rel.Info.IndexerRef; name != "" {
		var idx indexerSpecView
		if err := idx.get(ctx, c, ns, name); err == nil {
			o.IndexerPriority[name] = idx.priority
		} else {
			o.IndexerPriority[name] = defaultIndexerPriority
		}
	}
	return o
}

// resolveDelayProfile lists the namespace's DelayProfiles and runs spec
// §8.2's resolution order (item ref -> tag match -> lowest order) through the
// delayprofile controller's own pure Resolve.
//
// It resolves through a List rather than a client-side helper because
// delayprofile.Resolve is deliberately pure -- it takes the candidate slice,
// not a client. A namespace with no catch-all profile configured yields
// ErrNoMatch, which this treats as "no delay", not as a failure: the chart
// installs a catch-all, and an operator who removed it meant grabs to be
// immediate.
func resolveDelayProfile(ctx context.Context, c client.Client, ns string, ref *string, tags []string) (catalogv1alpha1.DelayProfileSpec, error) {
	var list catalogv1alpha1.DelayProfileList
	if err := c.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return catalogv1alpha1.DelayProfileSpec{}, fmt.Errorf("rssmatcher: list delay profiles: %w", err)
	}
	dp, err := delayprofile.Resolve(ref, tags, list.Items)
	if err != nil {
		logging.FromContext(ctx).Debug("rssmatcher: no delay profile applies; grabbing without a delay", "reason", err)
		return catalogv1alpha1.DelayProfileSpec{}, nil
	}
	return dp.Spec, nil
}

func resolveQualityProfile(ctx context.Context, c client.Client, name string, cat *catalogue.Catalogue) (quality.Profile, error) {
	var qp catalogv1alpha1.QualityProfile
	// QualityProfile is cluster-scoped (qualityprofile_types.go).
	if err := c.Get(ctx, client.ObjectKey{Name: name}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return quality.Profile{}, fmt.Errorf("rssmatcher: quality profile %q not found", name)
		}
		return quality.Profile{}, err
	}
	p, errs := quality.FromCRD(&qp, cat)
	if len(errs) > 0 {
		return quality.Profile{}, fmt.Errorf("rssmatcher: resolve quality profile %q: %w", name, errs[0])
	}
	return p, nil
}

type indexerSpecView struct{ priority int }

func (v *indexerSpecView) get(ctx context.Context, c client.Client, ns, name string) error {
	var idx indexv1alpha1.Indexer
	if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &idx); err != nil {
		return err
	}
	v.priority = int(idx.Spec.Priority)
	return nil
}
