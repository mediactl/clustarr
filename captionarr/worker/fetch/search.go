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
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/throttle"
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
	mediaPath string // logical /data path of the video
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

// search is spec §6.5's provider walk: "iterate providers by priority
// skipping throttled ... score with Bazarr weights; filter must/mustNot;
// download with fall-through; post-process ... write".
//
// Providers are asked in order and the first one that yields an acceptable
// candidate that downloads, post-processes and writes cleanly wins. Priority
// therefore decides which provider is SPENT first, not only ties: a
// lower-priority provider is never searched while a higher one answers with
// something good enough, which is what keeps OpenSubtitles' daily download
// quota for the files that need it. The upgrade pass later replaces a
// merely-acceptable subtitle with a better one from any provider, since its
// threshold is the current score plus one.
//
// Only errors the task cannot settle as a result are returned: a cancelled
// context, an unreadable throttle KV, and a [writeError].
func (w *Worker) search(ctx context.Context, m events.Message, p searchPlan) (searchOutcome, error) {
	var (
		out      searchOutcome
		lastBeat time.Time
	)
	kv := w.Bus.KV(events.BucketProviderThrottle)
	for _, ep := range p.providers {
		c, err := w.tryProvider(ctx, m, kv, ep, p, &out, &lastBeat)
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

// tryProvider searches one provider and downloads its best acceptable
// candidate, falling through its candidates in score order.
func (w *Worker) tryProvider(ctx context.Context, m events.Message, kv events.KV, ep eligibleProvider,
	p searchPlan, out *searchOutcome, lastBeat *time.Time,
) (*chosen, error) {
	e := ep.entry
	ctx, span := tracing.Start(ctx, "fetch.Worker.provider", trace.WithAttributes(
		attribute.String("provider", e.Name), attribute.String("provider.type", string(e.Type))))
	defer span.End()
	log := logging.FromContext(ctx).With("provider", e.Name)

	// Skip a throttled provider BEFORE spending a token on it
	// (captionarr/throttle's package doc: the two mechanisms are separate).
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

	for _, r := range rr.accepted {
		if err := w.beat(ctx, m, lastBeat); err != nil {
			return nil, err
		}
		if err := w.pace(ctx, kv, e.Local(), e.UID, e.RateMilli); err != nil {
			return nil, err
		}
		raw, name, err := ep.client.Download(ctx, r.c)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			out.fetchErrors = append(out.fetchErrors, fmt.Sprintf("%s: download %s: %v", e.Name, subtitleID(r.c), err))
			if !e.Local() && providerLevel(err) {
				// Quota, rate limit, auth, an unreachable host: every other
				// candidate from this provider would fail the same way, and
				// the recorded throttle now benches it for everyone.
				w.recordProviderError(ctx, kv, false, string(e.Type), e.UID, err)
				return nil, nil
			}
			continue
		}
		content, err := subtitles.PostProcess(raw, p.want.lang, p.mods, p.toSRT)
		if err != nil {
			// A subtitle file that does not decode or parse is that
			// candidate's defect; the next one may be fine.
			out.fetchErrors = append(out.fetchErrors, fmt.Sprintf("%s: %s (%s): %v", e.Name, subtitleID(r.c), name, err))
			continue
		}
		logical, rel, err := w.writeSidecar(ctx, p, content)
		if err != nil {
			return nil, &writeError{err: err}
		}
		log.Info("fetch: subtitle written", "subtitleID", subtitleID(r.c), "score", r.score, "path", logical)
		return &chosen{entry: ep, r: r, logicalPath: logical, relPath: rel}, nil
	}
	return nil, nil
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
	local, err := localPath(w.dataDir(), logical)
	if err != nil {
		return "", "", err
	}
	if err := subtitles.NewWriter().Write(ctx, local, content, w.sidecarMode()); err != nil {
		return "", "", err
	}
	return logical, filepath.Base(logical), nil
}

func isASS(content []byte) bool {
	head := content[:min(len(content), 4096)]
	return bytes.Contains(head, []byte("[Script Info]"))
}

// beat extends the delivery's ack deadline once heartbeatInterval has
// passed since the last one, mirroring importarr/worker/fileimport.
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
