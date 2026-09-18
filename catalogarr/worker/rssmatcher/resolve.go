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
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/delayprofile"
	"github.com/mediactl/clustarr/catalogarr/worker/grab"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"

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
	monitored        bool
	available        bool
	runtimeMinutes   int
	originalLanguage string
	qualityProfile   string
	delayProfileRef  *string
	tags             []string
	currentFile      *decision.Current
}

// resolve fetches the item named by ref and reads off everything the decision
// engine and the delay profile need.
//
// It is deliberately narrower than the search worker's own snapshot: the
// current file is read from the item's rolled-up status
// (hasFile/fileQuality/fileFormatScore) rather than from the MediaFile, and
// the already-imported source hash and title are not resolved.
// (reconciled by controller -- Task C12): once
// catalogarr/worker/search exports its snapshot builder, this should call it
// instead, so an RSS decision and a search decision see byte-identical input.
// The reduction is safe in one direction only -- it can approve an upgrade a
// fuller snapshot would have rejected as already-imported, and the grab's own
// lease plus activeDownloadRef re-read still stop a duplicate Download.
func resolve(ctx context.Context, c client.Client, ns string, ref commonv1.MediaRef, now time.Time) (resolveState, error) {
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
			st.originalLanguage = m.Status.Metadata.OriginalLanguage
		}
		st.currentFile = currentFrom(m.Status.HasFile, m.Status.FileQuality, m.Status.FileFormatScore)

	case commonv1.MediaKindEpisode, commonv1.MediaKindSeries:
		seriesName := ref.Name
		if ref.Kind == commonv1.MediaKindEpisode {
			var ep catalogv1alpha1.Episode
			if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &ep); err != nil {
				return st, err
			}
			seriesName = ep.Spec.SeriesRef
			st.monitored = ptr.Deref(ep.Spec.Monitored, true)
			st.available = ep.Status.AirDate != nil && !now.Before(ep.Status.AirDate.Time)
			st.currentFile = currentFrom(ep.Status.HasFile, ep.Status.FileQuality, ep.Status.FileFormatScore)
		} else {
			// A pack has no single file or air date of its own. It is
			// available because its episodes were matched, and its current
			// file is per-episode -- the importer re-checks each one.
			st.monitored = true
			st.available = true
		}
		var s catalogv1alpha1.Series
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: seriesName}, &s); err != nil {
			return st, fmt.Errorf("rssmatcher: get series %q: %w", seriesName, err)
		}
		st.qualityProfile = s.Spec.QualityProfileRef
		st.delayProfileRef = s.Spec.DelayProfileRef
		st.tags = s.Spec.Tags
		st.monitored = st.monitored && ptr.Deref(s.Spec.Monitored, true)
		if s.Status.Metadata != nil {
			st.runtimeMinutes = int(s.Status.Metadata.RuntimeMinutes)
			st.originalLanguage = s.Status.Metadata.OriginalLanguage
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

// queueFor lists the Downloads already working on ref, through the search
// worker's exported IndexDownloadTarget index. A read failure is a warning,
// not an error: an empty queue can only approve a release the fuller check
// might have deferred, and the grab's lease plus activeDownloadRef re-read
// still stop a second Download for the same item.
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

// blocklistFor builds decision.Target.Blocklist over the search worker's two
// exported blocklist indexes. Expiry is applied at read time, against this
// handler's clock, because a field index computed at write time would go
// stale the moment a deadline passed with nothing touching the object.
func blocklistFor(ctx context.Context, c client.Client, ns string, now time.Time) func(infohash, title string) bool {
	hit := func(index, value string) bool {
		if value == "" {
			return false
		}
		var list downloadv1alpha1.DownloadList
		if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{index: value}); err != nil {
			logging.FromContext(ctx).Warn("rssmatcher: blocklist lookup failed; treating the release as not blocklisted",
				"index", index, "error", err)
			return false
		}
		for i := range list.Items {
			until := list.Items[i].Status.BlocklistedUntil
			// A missing deadline means "forever": grabarr sets one when it
			// blocklists, and its absence is not a licence to grab again.
			if until == nil || until.After(now) {
				return true
			}
		}
		return false
	}
	return func(infohash, title string) bool {
		// Lower-cased so a v1 hash written in upper case by one indexer
		// still matches the same torrent reported in lower case by another,
		// matching how the search worker builds the index key.
		if hit(search.IndexBlocklistInfoHash, strings.ToLower(infohash)) {
			return true
		}
		return hit(search.IndexBlocklistTitle, release.CleanTitle(title))
	}
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
