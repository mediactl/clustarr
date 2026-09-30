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

package markers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// maxSegments is FileMarkers.Segments' MaxItems.
const maxSegments = 20

// maxMessage is FileMarkers.Message's MaxLength.
const maxMessage = 512

// errNotAsked is a NotFound decided without asking a provider.
var errNotAsked = errors.New("not asked")

// errNoProvider is a gateway whose registry has no markers provider: a
// disabled theintrodb, or the seed landing after the gateway built its
// registry. The task is acked without a result, so the file stays due and
// its next reconcile (a resync, or catalogarr's next start) publishes it
// again; a retry would cycle every due file to the dead-letter stream, and
// an Error would park it for ErrorTTL.
var errNoProvider = errors.New("no markers provider is configured")

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles;movies;episodes;series,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles/status,verbs=patch

// Handler is the catalogarr-markers durable's handler: it fetches one
// MediaFile's skip segments and records them in status.markers, the only
// field it writes, under k8s.ManagerCatalogarrMarkers.
type Handler struct {
	// Reader reads uncached: the MediaFile is read twice, the second time
	// just before the apply (the lost-update rule).
	Reader    client.Reader
	Providers []metadata.MarkersProvider
	Clock     func() time.Time
	// Apply writes the status; nil applies through k8s.PatchStatus with
	// Client.
	Apply  func(ctx context.Context, ac *catalogac.MediaFileApplyConfiguration) error
	Client client.Client
}

// Handle implements events.Handler.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var task schema.MarkersTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable markers task", err)
	}
	ns, _, ok := strings.Cut(env.Key, "/")
	if !ok || task.MediaFile == "" {
		return events.Discard("envelope key is not <namespace>/<name>", fmt.Errorf("key=%q", env.Key))
	}
	now := h.now()
	var mf catalogv1alpha1.MediaFile
	if err := h.Reader.Get(ctx, client.ObjectKey{Namespace: ns, Name: task.MediaFile}, &mf); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if due, _ := Due(&mf, now); !due {
		return nil // a duplicate or redelivery for a file already recorded
	}

	q, err := h.query(ctx, &mf)
	var segs metadata.Segments
	if err == nil {
		segs, err = h.ask(ctx, q)
	}
	var rl *metadata.RateLimitedError
	if errors.As(err, &rl) {
		return events.Retry(rl.RetryAfter, err) // a limit is not a result
	}
	if errors.Is(err, errNoProvider) {
		return nil // the registry, not the file, is missing something
	}

	// The lost-update rule: the file is read again just before the apply,
	// and a file re-probed meanwhile is left to the fetch its new probe asks for.
	var fresh catalogv1alpha1.MediaFile
	if gerr := h.Reader.Get(ctx, client.ObjectKeyFromObject(&mf), &fresh); gerr != nil {
		if apierrors.IsNotFound(gerr) {
			return nil
		}
		return gerr
	}
	if fresh.Status.ProbeHash != mf.Status.ProbeHash {
		return nil
	}

	ac := catalogac.FileMarkers().
		WithFetchedAt(metav1.NewTime(now)).
		WithForProbeHash(mf.Status.ProbeHash).
		WithDurationMs(q.DurationMs)
	switch {
	case err == nil:
		ac.WithResult(catalogv1alpha1.MarkersFound).WithSegments(segmentACs(segs)...)
	case errors.Is(err, metadata.ErrNotFound):
		ac.WithResult(catalogv1alpha1.MarkersNotFound)
		if errors.Is(err, errNotAsked) { // decided here, so say why
			ac.WithMessage(clamp(err.Error()))
		}
	default:
		logging.FromContext(ctx).WarnContext(ctx, "markers: fetch failed", "mediafile", mf.Name, "error", err)
		ac.WithResult(catalogv1alpha1.MarkersError).WithMessage(clamp(err.Error()))
	}
	return h.apply(ctx, catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(catalogac.MediaFileStatus().WithMarkers(ac)))
}

// query names the file to TheIntroDB: a movie by its TMDB id, an episode by
// its series' TVDB id, season and episode -- only in the aired order, the
// one TheIntroDB numbers episodes in.
func (h *Handler) query(ctx context.Context, mf *catalogv1alpha1.MediaFile) (metadata.MarkersQuery, error) {
	q := metadata.MarkersQuery{DurationMs: mf.Status.MediaInfo.RuntimeMillis}
	key := client.ObjectKey{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		var mv catalogv1alpha1.Movie
		if err := h.Reader.Get(ctx, key, &mv); err != nil {
			return q, err
		}
		q.IDs = metadata.ExternalIDs{metadata.KeyTMDB: strconv.FormatInt(mv.Spec.TmdbID, 10)}
	case commonv1.MediaKindEpisode:
		if others := otherEpisodes(mf.Spec.MediaRef); others > 0 {
			return q, fmt.Errorf("the file holds %d more episodes, whose segments TheIntroDB times per episode: %w: %w",
				others, errNotAsked, metadata.ErrNotFound)
		}
		var ep catalogv1alpha1.Episode
		if err := h.Reader.Get(ctx, key, &ep); err != nil {
			return q, err
		}
		var sr catalogv1alpha1.Series
		if err := h.Reader.Get(ctx, client.ObjectKey{Namespace: mf.Namespace, Name: ep.Spec.SeriesRef}, &sr); err != nil {
			return q, err
		}
		if order := series.EffectiveEpisodeOrder(sr.Spec.SeriesType, sr.Spec.EpisodeOrder); order != catalogv1alpha1.EpisodeOrderOfficial {
			return q, fmt.Errorf("episode order %s is not the aired order TheIntroDB numbers episodes in: %w: %w",
				order, errNotAsked, metadata.ErrNotFound)
		}
		q.IDs = metadata.ExternalIDs{metadata.KeyTVDB: strconv.FormatInt(sr.Spec.TvdbID, 10)}
		q.Season, q.Episode = ep.Spec.SeasonNumber, ep.Spec.EpisodeNumber
	default:
		return q, fmt.Errorf("%s files have no markers: %w: %w", mf.Spec.MediaRef.Kind, errNotAsked, metadata.ErrNotFound)
	}
	return q, nil
}

// otherEpisodes counts the episodes a file's ref names besides its own.
func otherEpisodes(ref commonv1.MediaRef) int {
	n := 0
	for _, k := range ref.Keys {
		if k != ref.Name {
			n++
		}
	}
	return n
}

// ask takes the first provider's answer; NotFound from every one is
// NotFound, and any other failure is the error.
func (h *Handler) ask(ctx context.Context, q metadata.MarkersQuery) (metadata.Segments, error) {
	if len(h.Providers) == 0 {
		return metadata.Segments{}, errNoProvider
	}
	var last error
	for _, p := range h.Providers {
		segs, err := p.Markers(ctx, q)
		if err == nil {
			return segs, nil
		}
		last = err
		if !errors.Is(err, metadata.ErrNotFound) {
			return metadata.Segments{}, err
		}
	}
	return metadata.Segments{}, last
}

func (h *Handler) apply(ctx context.Context, ac *catalogac.MediaFileApplyConfiguration) error {
	if h.Apply != nil {
		return h.Apply(ctx, ac)
	}
	_, err := k8s.PatchStatus(ctx, h.Client, k8s.ManagerCatalogarrMarkers, ac)
	return err
}

func (h *Handler) now() time.Time {
	if h.Clock != nil {
		return h.Clock()
	}
	return time.Now()
}

// segmentACs flattens the segments by start, capped at the CRD's MaxItems.
func segmentACs(s metadata.Segments) []*catalogac.MarkerSegmentApplyConfiguration {
	type seg struct {
		kind catalogv1alpha1.MarkerKind
		s    metadata.Segment
	}
	var all []seg
	for _, k := range []struct {
		kind catalogv1alpha1.MarkerKind
		list []metadata.Segment
	}{
		{catalogv1alpha1.MarkerIntro, s.Intro},
		{catalogv1alpha1.MarkerRecap, s.Recap},
		{catalogv1alpha1.MarkerCredits, s.Credits},
		{catalogv1alpha1.MarkerPreview, s.Preview},
	} {
		for _, x := range k.list {
			all = append(all, seg{k.kind, x})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].s.StartMs < all[j].s.StartMs })
	if len(all) > maxSegments {
		all = all[:maxSegments]
	}
	out := make([]*catalogac.MarkerSegmentApplyConfiguration, len(all))
	for i, x := range all {
		out[i] = catalogac.MarkerSegment().WithKind(x.kind).WithStartMs(x.s.StartMs).WithEndMs(x.s.EndMs)
	}
	return out
}

// clamp cuts s to the CRD's MaxLength on a rune boundary.
func clamp(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	cut := maxMessage
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }
