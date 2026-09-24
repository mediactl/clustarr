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

package fetch

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/datapath"
	"github.com/mediactl/clustarr/app/caption/throttle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// searchPlan is everything one task's provider walk needs.
type searchPlan struct {
	kind      commonv1.MediaKind
	query     subtitles.Query
	want      want
	filters   filters
	threshold int
	providers []eligibleProvider
	skipped   []string
	mods      []string
	toSRT     bool
	hiExt     string
	mediaPath string      // logical /data path of the video
	mode      os.FileMode // the sidecar's file mode ([Worker.sidecarModeFor])
}

// chosen is the candidate that was downloaded and written.
type chosen struct {
	entry       eligibleProvider
	r           ranked
	logicalPath string // logical /data path of the written sidecar
	relPath     string // sidecar path relative to the media file's directory
}

// searchOutcome is what the provider walk found, for [decide] and the
// item's lastError.
type searchOutcome struct {
	chosen *chosen

	// searched counts providers that answered a search (an empty answer
	// included).
	searched int
	// throttled lists providers skipped for an active throttle window, and
	// earliest is the soonest of those windows' ends.
	throttled []string
	earliest  time.Time
	// providerErrors are provider-level failures, each recorded in the
	// shared throttle.
	providerErrors []string
	// acceptable counts candidates at or above the threshold; fetchErrors
	// are why none of them made it to disk.
	acceptable  int
	fetchErrors []string
	// bestBelow is the best score among filtered-in candidates that missed
	// the threshold, and bestBelowFrom the provider that offered it.
	bestBelow     int
	bestBelowFrom string
}

// writeError marks a failure to write the sidecar itself -- a local disk
// problem, not a provider's -- so the caller redelivers instead of moving
// on to another provider that would hit the same disk.
type writeError struct{ err error }

func (e *writeError) Error() string { return "write sidecar: " + e.err.Error() }
func (e *writeError) Unwrap() error { return e.err }

// search is spec §6.5's provider walk -- "iterate providers by priority
// skipping throttled ... score with Bazarr weights; filter must/mustNot;
// download with fall-through" -- done Bazarr's way (gap-fix ruling R-4):
// every eligible provider is searched, all their candidates are scored and
// ranked together as one pool, and the best is downloaded, falling through
// the pool in rank order until one downloads, post-processes and writes
// cleanly. This is subliminal_patch's list_all_subtitles followed by
// download_best_subtitles (research note §5); until gap-fix X11b the first
// provider with an acceptable candidate won, so a better subtitle from a
// lower-priority provider was never even seen.
//
// Priority -- or the profile's spec.providers order -- breaks score ties,
// as the provider order does in Bazarr's stable sort; it no longer decides
// which provider is searched. A search is cheap and paced by the shared
// token bucket; the scarce thing, a provider's daily DOWNLOAD quota, is still
// spent on exactly one candidate per task, the pool's best.
//
// Each SubtitleProvider is its own provider here, so two of one type -- two
// accounts -- are both searched. Their answers may overlap; that is how the
// second account's quota backs the first's: when a download fails at the
// provider level the provider is benched for the rest of the task (and in
// the shared throttle), and the pool falls through to the same subtitle
// from the other account.
//
// Local providers (embedded) form a tier of their own and go first. They
// cost no upstream request, and they are only eligible when the profile's
// spec.embedded.extract is on -- whose documented meaning is "writes a
// matching embedded track out as a sidecar INSTEAD OF searching providers
// for it" -- so when an extractable track reaches the threshold and writes,
// no remote provider is asked at all. Only when the local tier writes
// nothing are the remote providers searched and pooled.
//
// Only errors the task cannot settle as a result are returned: a cancelled
// context, an unreadable throttle KV, and a [writeError].
func (w *Worker) search(ctx context.Context, m events.Message, p searchPlan) (searchOutcome, error) {
	var (
		out      searchOutcome
		lastBeat time.Time
	)
	kv := w.Bus.KV(events.BucketProviderThrottle)
	for _, tier := range tiers(p.providers) {
		pool, err := w.gather(ctx, m, kv, p, tier, &out, &lastBeat)
		if err != nil {
			return out, err
		}
		c, err := w.fetchBest(ctx, m, kv, p, pool, &out, &lastBeat)
		if err != nil {
			return out, err
		}
		if c != nil {
			out.chosen = c
			return out, nil
		}
	}
	return out, nil
}

// pooled is one acceptable candidate in a tier's pool.
type pooled struct {
	// provider indexes searchPlan.providers: the provider that offered the
	// candidate, and its rank in the priority order that breaks ties.
	provider int
	r        ranked
}

// tiers splits the plan's providers into the local tier and the remote
// tier, in that order, each keeping the plan's priority order. An empty
// tier is left out.
func tiers(ps []eligibleProvider) [][]int {
	var local, remote []int
	for i, ep := range ps {
		if ep.entry.Local() {
			local = append(local, i)
		} else {
			remote = append(remote, i)
		}
	}
	var out [][]int
	for _, t := range [][]int{local, remote} {
		if len(t) > 0 {
			out = append(out, t)
		}
	}
	return out
}

// gather searches every provider in tier and pools their acceptable
// candidates, best first ([rankPool]).
func (w *Worker) gather(ctx context.Context, m events.Message, kv events.KV, p searchPlan, tier []int,
	out *searchOutcome, lastBeat *time.Time,
) ([]pooled, error) {
	var pool []pooled
	for _, i := range tier {
		accepted, err := w.searchProvider(ctx, m, kv, p.providers[i], p, out, lastBeat)
		if err != nil {
			return nil, err
		}
		for _, r := range accepted {
			pool = append(pool, pooled{provider: i, r: r})
		}
	}
	rankPool(pool)
	return pool, nil
}

// rankPool orders a pool the way Bazarr's download_best_subtitles does:
// score, then score without the hash, descending -- a stable sort over the
// providers' own order, so priority breaks what is left -- then the
// candidate's download count and id, so the order is deterministic.
func rankPool(pool []pooled) {
	slices.SortStableFunc(pool, func(a, b pooled) int {
		return cmp.Or(
			cmp.Compare(b.r.score, a.r.score),
			cmp.Compare(b.r.without, a.r.without),
			cmp.Compare(a.provider, b.provider),
			cmp.Compare(b.r.c.Downloads, a.r.c.Downloads),
			cmp.Compare(subtitleID(a.r.c), subtitleID(b.r.c)),
		)
	})
}

// searchProvider asks one provider and returns its candidates at or above
// the threshold, scored. A throttled provider is skipped before a token is
// spent on it; a provider error is recorded in the shared throttle and
// settles as "nothing from this provider".
func (w *Worker) searchProvider(ctx context.Context, m events.Message, kv events.KV, ep eligibleProvider,
	p searchPlan, out *searchOutcome, lastBeat *time.Time,
) ([]ranked, error) {
	e := ep.entry
	ctx, span := tracing.Start(ctx, "fetch.Worker.search", trace.WithAttributes(
		attribute.String("provider", e.Name), attribute.String("provider.type", string(e.Type))))
	defer span.End()
	log := logging.FromContext(ctx).With("provider", e.Name)

	// Skip a throttled provider BEFORE spending a token on it
	// (app/caption/throttle's package doc: the two mechanisms are separate).
	if !e.Local() {
		st, err := throttle.Get(ctx, kv, e.UID)
		if err != nil {
			return nil, fmt.Errorf("read throttle for %s: %w", e.Name, err)
		}
		if st.Throttled(w.now()) {
			until := *st.ThrottledUntil
			out.throttled = append(out.throttled, fmt.Sprintf("%s until %s (%s)",
				e.Name, until.UTC().Format(time.RFC3339), st.ThrottleReason))
			if out.earliest.IsZero() || until.Before(out.earliest) {
				out.earliest = until
			}
			log.Debug("fetch: provider is throttled; skipping", "until", until)
			return nil, nil
		}
	}

	if err := w.beat(ctx, m, lastBeat); err != nil {
		return nil, err
	}
	if err := w.pace(ctx, kv, e.Local(), e.UID, e.RateMilli); err != nil {
		return nil, err
	}
	cands, err := ep.client.Search(ctx, p.query)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if subtitles.IsNotFound(err) {
			// "This show does not exist upstream" is about this item, not
			// the provider: pkg/subtitles.ThrottleFor would bench the whole
			// provider for 12h on it, for every other file too.
			out.searched++
			log.Debug("fetch: provider does not know this item", "err", err)
			return nil, nil
		}
		tracing.RecordError(span, err)
		out.providerErrors = append(out.providerErrors, fmt.Sprintf("%s: %v", e.Name, err))
		w.recordProviderError(ctx, kv, e.Local(), string(e.Type), e.UID, err)
		return nil, nil
	}
	out.searched++
	w.recordSuccess(ctx, kv, e.Local(), e.UID)

	caps := ep.client.Capabilities()
	rr := rank(rankInput{
		kind:           p.kind,
		providerType:   e.Type,
		hashVerifiable: caps.HashVerifiable,
		hiVerifiable:   ep.client.HIVerifiable(),
		query:          p.query,
		want:           p.want,
		filters:        p.filters.withProviderOptions(e.Options),
		threshold:      p.threshold,
	}, cands)
	if rr.bestBelow > out.bestBelow {
		out.bestBelow, out.bestBelowFrom = rr.bestBelow, e.Name
	}
	out.acceptable += len(rr.accepted)
	log.Debug("fetch: provider answered", "candidates", len(cands), "filtered", rr.filtered,
		"accepted", len(rr.accepted), "threshold", p.threshold)
	return rr.accepted, nil
}

// fetchBest downloads the pool's best candidate, falling through in rank
// order: a candidate that fails to download or to post-process is that
// candidate's defect and the next one is tried; a provider-level failure
// (quota, rate limit, auth, an unreachable host) benches that provider for
// the rest of the pool, since every other candidate from it would fail the
// same way -- and the recorded throttle benches it for every worker.
func (w *Worker) fetchBest(ctx context.Context, m events.Message, kv events.KV, p searchPlan, pool []pooled,
	out *searchOutcome, lastBeat *time.Time,
) (*chosen, error) {
	benched := map[int]bool{}
	for _, pc := range pool {
		if benched[pc.provider] {
			continue
		}
		ep := p.providers[pc.provider]
		c, benchedNow, err := w.fetchOne(ctx, m, kv, p, ep, pc.r, out, lastBeat)
		if err != nil || c != nil {
			return c, err
		}
		if benchedNow {
			benched[pc.provider] = true
		}
	}
	return nil, nil
}

// fetchOne downloads, post-processes and writes one candidate. It returns
// the written sidecar, or whether the provider is now benched, or an error
// the task cannot settle as a result.
func (w *Worker) fetchOne(ctx context.Context, m events.Message, kv events.KV, p searchPlan, ep eligibleProvider,
	r ranked, out *searchOutcome, lastBeat *time.Time,
) (*chosen, bool, error) {
	e := ep.entry
	ctx, span := tracing.Start(ctx, "fetch.Worker.download", trace.WithAttributes(
		attribute.String("provider", e.Name), attribute.String("provider.type", string(e.Type))))
	defer span.End()

	if err := w.beat(ctx, m, lastBeat); err != nil {
		return nil, false, err
	}
	if err := w.pace(ctx, kv, e.Local(), e.UID, e.RateMilli); err != nil {
		return nil, false, err
	}
	raw, name, err := ep.client.Download(ctx, r.c)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		tracing.RecordError(span, err)
		out.fetchErrors = append(out.fetchErrors, fmt.Sprintf("%s: download %s: %v", e.Name, subtitleID(r.c), err))
		if !e.Local() && providerLevel(err) {
			w.recordProviderError(ctx, kv, false, string(e.Type), e.UID, err)
			return nil, true, nil
		}
		return nil, false, nil
	}
	content, err := subtitles.PostProcess(raw, p.want.lang, p.mods, p.toSRT)
	if err != nil {
		// A subtitle file that does not decode or parse is that
		// candidate's defect; the next one may be fine.
		out.fetchErrors = append(out.fetchErrors, fmt.Sprintf("%s: %s (%s): %v", e.Name, subtitleID(r.c), name, err))
		return nil, false, nil
	}
	logical, rel, err := w.writeSidecar(ctx, p, content)
	if err != nil {
		return nil, false, &writeError{err: err}
	}
	logging.FromContext(ctx).Info("fetch: subtitle written", "provider", e.Name,
		"subtitleID", subtitleID(r.c), "score", r.score, "path", logical)
	return &chosen{entry: ep, r: r, logicalPath: logical, relPath: rel}, false, nil
}

// pace takes one token from the provider's shared bucket before a request.
// A local provider makes no upstream request and is not paced.
func (w *Worker) pace(ctx context.Context, kv events.KV, local bool, uid string, rateMilli int32) error {
	if local {
		return nil
	}
	if err := throttle.Acquire(ctx, kv, uid, rateMilli); err != nil {
		return fmt.Errorf("acquire provider token: %w", err)
	}
	return nil
}

// recordProviderError merges a provider failure into the shared throttle
// (ruling R2: the KV, never SubtitleProvider.status), plus the reported
// quota for a download-limit error. A failure to record is logged, not
// returned: it costs one provider's backoff accuracy, and failing the task
// over it would redeliver a search that already reached a verdict.
func (w *Worker) recordProviderError(ctx context.Context, kv events.KV, local bool, providerType, uid string, cause error) {
	if local {
		return
	}
	log := logging.FromContext(ctx)
	st, err := throttle.RecordError(ctx, kv, providerType, uid, cause, w.now())
	if err != nil {
		log.Warn("fetch: could not record a provider error in the throttle table", "err", err, "cause", cause)
		return
	}
	var pe *subtitles.ProviderError
	if subtitles.IsQuotaExceeded(cause) && errors.As(cause, &pe) {
		remaining := int32(min(max(pe.Remaining, 0), math.MaxInt32)) //nolint:gosec // clamped to int32's range
		if _, err := throttle.SetQuota(ctx, kv, uid, remaining, pe.ResetAt); err != nil {
			log.Warn("fetch: could not record a provider quota", "err", err)
		}
	}
	log.Warn("fetch: provider error; throttled for every worker", "err", cause,
		"throttledUntil", st.ThrottledUntil, "reason", st.ThrottleReason)
}

// recordSuccess stamps the provider's last success, best effort.
func (w *Worker) recordSuccess(ctx context.Context, kv events.KV, local bool, uid string) {
	if local {
		return
	}
	if _, err := throttle.RecordSuccess(ctx, kv, uid, w.now()); err != nil {
		logging.FromContext(ctx).Warn("fetch: could not record a provider success", "err", err)
	}
}

// providerLevel reports whether a download error is about the provider
// rather than the one candidate: a classified ProviderError, or a transport
// failure reaching it. An oversized body or an unusable fetch handle is the
// candidate's own problem.
func providerLevel(err error) bool {
	var pe *subtitles.ProviderError
	if errors.As(err, &pe) {
		return true
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// writeSidecar writes content next to the video, named by
// pkg/subtitles.SidecarName, atomically. SidecarName always says ".srt";
// when the profile keeps the original format and the content is still ASS,
// the extension follows the content so a player does not misparse it.
func (w *Worker) writeSidecar(ctx context.Context, p searchPlan, content []byte) (logical, rel string, err error) {
	logical = subtitles.SidecarName(p.mediaPath, p.want.key, p.hiExt)
	if !p.toSRT && isASS(content) {
		logical = strings.TrimSuffix(logical, ".srt") + ".ass"
	}
	local, err := datapath.Local(w.DataDir, logical)
	if err != nil {
		return "", "", err
	}
	if err := subtitles.NewWriter().Write(ctx, local, content, cmp.Or(p.mode, DefaultSidecarMode)); err != nil {
		return "", "", err
	}
	return logical, filepath.Base(logical), nil
}

func isASS(content []byte) bool {
	head := content[:min(len(content), 4096)]
	return bytes.Contains(head, []byte("[Script Info]"))
}

// beat extends the delivery's ack deadline once heartbeatInterval has
// passed since the last one, mirroring app/import/worker/fileimport.
func (w *Worker) beat(ctx context.Context, m events.Message, last *time.Time) error {
	now := w.now()
	if !last.IsZero() && now.Sub(*last) < heartbeatInterval {
		return nil
	}
	*last = now
	if err := m.InProgress(ctx); err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	return nil
}
