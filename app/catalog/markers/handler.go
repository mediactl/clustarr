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
	"strconv"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/segmenting"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/segments"
)

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
	// Bus re-publishes a task deferred to a spent key's reset.
	Bus events.Publisher
	// KV is the segments bucket: the file's own analysis, merged under
	// TheIntroDB's segments. Nil merges none.
	KV events.KV

	// absent remembers, for SeriesAbsentTTL, each series the provider has
	// nothing for at all (metadata.ErrNoTitle), keyed by its query id, so
	// its other episodes are not asked about one by one.
	mu     sync.Mutex
	absent map[string]time.Time
}

// deferAfter is the longest limit a task waits out in its in-flight slot.
// A longer one -- TheIntroDB's daily allowance -- is published again for
// the reset, so the durable's slots serve the tasks that need no request.
const deferAfter = 5 * time.Minute

// SeriesAbsentTTL is how long a series the provider lacks answers for its
// other episodes without a request. The gateway runs one replica, and a
// restart costs one request per series.
const SeriesAbsentTTL = 24 * time.Hour

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
		if h.seriesAbsent(q, now) {
			err = fmt.Errorf("the series is not on TheIntroDB: %w: %w", errNotAsked, metadata.ErrNotFound)
		} else {
			segs, err = h.ask(ctx, q)
			if errors.Is(err, metadata.ErrNoTitle) {
				h.rememberAbsent(q, now)
			}
		}
	}
	var rl *metadata.RateLimitedError
	if errors.As(err, &rl) { // a limit is not a result
		if rl.RetryAfter > deferAfter && h.Bus != nil {
			if perr := PublishAt(ctx, h.Bus, &mf, now, now.Add(rl.RetryAfter)); perr != nil {
				return events.Retry(rl.RetryAfter, errors.Join(err, perr))
			}
			return nil // the slot goes to the next task; this one returns at the reset
		}
		return events.Retry(rl.RetryAfter, err)
	}
	if errors.Is(err, errNoProvider) {
		return nil // the registry, not the file, is missing something
	}

	// The apply re-reads the file and drops the result when it was
	// re-probed meanwhile (the lost-update rule), then merges TheIntroDB's
	// segments with the file's own analysis (segmenting.ApplyMerged).
	tid := &segmenting.TheIntroDBUpdate{
		FetchedAt: metav1.NewTime(now), ForProbeHash: mf.Status.ProbeHash, DurationMs: q.DurationMs,
	}
	switch {
	case err == nil:
		tid.Result, tid.Segments = catalogv1alpha1.MarkersFound, toSegments(segs)
	case errors.Is(err, metadata.ErrNotFound):
		since := metav1.NewTime(notFoundSince(&mf, now))
		tid.Result, tid.NotFoundSince = catalogv1alpha1.MarkersNotFound, &since
		if errors.Is(err, errNotAsked) { // decided here, so say why
			tid.Message = clamp(err.Error())
		}
	default:
		logging.FromContext(ctx).WarnContext(ctx, "markers: fetch failed", "mediafile", mf.Name, "error", err)
		tid.Result, tid.Message = catalogv1alpha1.MarkersError, clamp(err.Error())
	}
	applier := &segmenting.Applier{Reader: h.Reader, KV: h.KV, Apply: h.apply}
	return applier.ApplyMerged(ctx, client.ObjectKeyFromObject(&mf), tid, nil)
}

// notFoundSince is when the provider first had nothing for mf's probe: the
// last result's, when it was a NotFound for the same probe, else now.
func notFoundSince(mf *catalogv1alpha1.MediaFile, now time.Time) time.Time {
	prev := mf.Status.Markers
	if prev == nil || prev.Result != catalogv1alpha1.MarkersNotFound || prev.ForProbeHash != mf.Status.ProbeHash {
		return now
	}
	if prev.NotFoundSince != nil {
		return prev.NotFoundSince.Time
	}
	return prev.FetchedAt.Time
}

// seriesKey names an episode query's series; "" for a movie.
func seriesKey(q metadata.MarkersQuery) string {
	if q.Season == 0 && q.Episode == 0 {
		return ""
	}
	return metadata.KeyTVDB + ":" + q.IDs[metadata.KeyTVDB]
}

func (h *Handler) seriesAbsent(q metadata.MarkersQuery, now time.Time) bool {
	k := seriesKey(q)
	if k == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	at, ok := h.absent[k]
	return ok && now.Sub(at) < SeriesAbsentTTL
}

func (h *Handler) rememberAbsent(q metadata.MarkersQuery, now time.Time) {
	k := seriesKey(q)
	if k == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.absent == nil {
		h.absent = map[string]time.Time{}
	}
	h.absent[k] = now
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

// toSegments tags TheIntroDB's segments with their source, confidence 100;
// segments.Merge orders and caps them.
func toSegments(s metadata.Segments) []segments.Segment {
	var out []segments.Segment
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
			out = append(out, segments.Segment{
				Kind: k.kind, StartMs: x.StartMs, EndMs: x.EndMs,
				Source: catalogv1alpha1.SegmentSourceTheIntroDB, Confidence: 100,
			})
		}
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
