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
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// errUnsupportedKind is a matched target of a kind the matcher cannot
// resolve.
var errUnsupportedKind = fmt.Errorf("rssmatcher: unsupported kind")

// resolveState is what the release rules need about one matched item, and
// where its candidate goes. Since ADR-0019 (§6.6, A4.4) it reads no current
// file, queue, delay profile or indexer priority: the owner's grab planner
// re-checks those against fresh state.
type resolveState struct {
	monitored      bool
	available      bool
	runtimeMinutes int
	// originalLanguageTag is the BCP-47 tag verbatim from
	// status.metadata.originalLanguage. Never a display name.
	originalLanguageTag string
	qualityProfile      string
	// identity is built by app/catalog/worker/search's own MovieIdentity,
	// EpisodeIdentity and (through search.ReadNonVideo) the non-video
	// builders, so an RSS decision and a search decision agree on what the
	// item is.
	identity decision.Identity
	// owner is the grab owner (ADR-0019 §6.1): the item itself, an
	// Episode's or a pack's Series, an Issue's Comic. targets are the
	// matched Episodes or Issues under a container owner.
	owner   schema.ItemRef
	targets []schema.ItemRef
	// scope is the owner's release-index block scope (§6.14).
	scope string
}

func itemRef(kind string, o client.Object) schema.ItemRef {
	return schema.ItemRef{Kind: kind, Ref: schema.Ref{Namespace: o.GetNamespace(), Name: o.GetName(), UID: string(o.GetUID())}}
}

// resolve fetches the item named by ref and reads off what the release
// rules need and where the candidate goes.
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
		if m.Status.Metadata != nil {
			st.runtimeMinutes = int(m.Status.Metadata.RuntimeMinutes)
			st.originalLanguageTag = m.Status.Metadata.OriginalLanguage
		}
		st.identity = search.MovieIdentity(&m)
		st.owner = itemRef(string(commonv1.MediaKindMovie), &m)

	case commonv1.MediaKindEpisode, commonv1.MediaKindSeries:
		seriesName := ref.Name
		names := ref.Keys
		if ref.Kind == commonv1.MediaKindEpisode {
			var ep catalogv1alpha1.Episode
			if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &ep); err != nil {
				return st, err
			}
			seriesName, names = ep.Spec.SeriesRef, []string{ep.Name}
		}
		// A pack's numbering is exactly the episodes the matcher resolved
		// it to (packRef's Keys): without them a pack target has no
		// numbering and every pack release fails closed as UnknownItem.
		var eps []*catalogv1alpha1.Episode
		st.monitored, st.available = true, true
		for _, name := range names {
			var ep catalogv1alpha1.Episode
			if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &ep); err != nil {
				if apierrors.IsNotFound(err) && ref.Kind == commonv1.MediaKindSeries {
					continue
				}
				return st, err
			}
			eps = append(eps, &ep)
			st.targets = append(st.targets, itemRef(string(commonv1.MediaKindEpisode), &ep))
			if ref.Kind == commonv1.MediaKindEpisode {
				st.monitored = ptr.Deref(ep.Spec.Monitored, true)
				st.available = ep.Status.AirDate != nil && !now.Before(ep.Status.AirDate.Time)
			}
		}
		var s catalogv1alpha1.Series
		if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: seriesName}, &s); err != nil {
			return st, fmt.Errorf("rssmatcher: get series %q: %w", seriesName, err)
		}
		st.identity = search.EpisodeIdentity(&s, eps...)
		// The series' whole scene table, read by the same code as the
		// search worker's. SingleEpisodeSearch stays false: an RSS release
		// was not asked for anything, and a pack that covers the episodes
		// it was matched to is exactly what it may grab.
		st.identity.SceneMappings = scenes.table(ctx, s.Spec.TvdbID)
		st.qualityProfile = s.Spec.QualityProfileRef
		st.monitored = st.monitored && ptr.Deref(s.Spec.Monitored, true)
		if s.Status.Metadata != nil {
			st.runtimeMinutes = int(s.Status.Metadata.RuntimeMinutes)
			st.originalLanguageTag = s.Status.Metadata.OriginalLanguage
		}
		st.owner = itemRef("Series", &s)

	case commonv1.MediaKindAlbum, commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindIssue:
		v, err := search.ReadNonVideo(ctx, c, ns, ref, now)
		if err != nil {
			return st, err
		}
		st.monitored = v.Monitored
		st.available = v.Available
		st.qualityProfile = v.QualityProfileRef
		st.identity = v.Identity
		st.owner = v.Owner
		if ref.Kind == commonv1.MediaKindIssue {
			var iss catalogv1alpha1.Issue
			if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &iss); err != nil {
				return st, err
			}
			st.targets = []schema.ItemRef{itemRef("Issue", &iss)}
		}

	default:
		return st, fmt.Errorf("%w: %s", errUnsupportedKind, ref.Kind)
	}
	if st.owner.UID != "" {
		st.scope = schema.BlockScopeOf(st.owner)
	}
	return st, nil
}

// releaseOptions are the release rules' options for a firehose release:
// both protocols, since which are enabled -- and the indexer's priority --
// are the owner's grab planner's now (P22).
func releaseOptions() decision.Options {
	return decision.Options{
		ProtocolsEnabled: map[string]bool{
			string(commonv1.ProtocolTorrent): true,
			string(commonv1.ProtocolUsenet):  true,
		},
	}
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
