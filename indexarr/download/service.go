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

package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// The closed set of metric outcome values. An indexer-supplied string -- a
// Content-Type, a tracker error, a GUID -- must NEVER reach a label.
const (
	resultOK             = "ok"
	resultMagnet         = "magnet"
	resultRedirect       = "redirect"
	resultNoURL          = "no_url"
	resultBadRequest     = "bad_request"
	resultNotFound       = "not_found"
	resultDisabled       = "disabled"
	resultUnauthorized   = "unauthorized"
	resultHTTPError      = "http_error"
	resultTransport      = "transport_error"
	resultTooLarge       = "too_large"
	resultInvalidPayload = "invalid_payload"
	resultNotConfigured  = "not_configured"
	resultGrabCounted    = "grab_counted"
	resultGrabDuplicate  = "grab_duplicate"
	resultGrabFailed     = "grab_count_failed"
)

// unknownIndexerLabel keeps an unresolved request off the metric's label
// space: req.IndexerRef.Name is caller-supplied and unbounded, and the number
// of distinct values a bad caller could mint is not.
const unknownIndexerLabel = "_unknown"

// maxGUIDLogChars bounds a GUID before it reaches a log line. A GUID is very
// often the release's details URL, passkey included, so it is redacted like a
// URL first -- and it never reaches a metric label at all.
const maxGUIDLogChars = 128

// Service is the rpc.indexarr.download handler.
type Service struct {
	// Client is the manager's cached client; it reads Indexer and Secret and
	// is the writer of status.grabsInWindow.
	Client client.Client

	// Bus carries the grab ring in clustarr-indexer-limits. A nil Bus
	// disables accounting rather than failing the grab.
	Bus events.Bus

	// Fetch builds the Fetcher for one spec.generic Indexer. D1-8 supplies
	// NewFetcherFor; a test supplies a stub.
	Fetch FetcherFor

	// Definitions builds the Fetcher for a definition-backed Indexer
	// (spec.definition or spec.definitionRef). Production supplies
	// indexer.ClientCache.DefinitionFetcherFor, which dispatches to
	// cardigann.Engine.Download. A nil Definitions REFUSES a
	// definition-backed grab rather than falling back to Fetch: a plain GET
	// skips the definition's download block (its before-request and link
	// selectors), and for most trackers the link a search returned is a
	// details page, so the fallback would hand grabarr an HTML page.
	Definitions FetcherFor

	// Now is the clock. nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// fail builds the ONE shape a failure takes: a populated Error, scrubbed of
// this indexer's secret values and truncated, because grabarr writes it onto
// a Download's status condition where a human reads it.
func fail(scrub func(string) string, format string, args ...any) schema.DownloadResponse {
	msg := fmt.Sprintf(format, args...)
	if scrub != nil {
		msg = scrub(msg)
	}
	return schema.DownloadResponse{Error: truncate(msg, maxErrorChars)}
}

// Handle is the rpc.indexarr.download body. It NEVER returns an error: after a
// successful decode every failure is a populated DownloadResponse.Error, so
// the caller can distinguish "this release could not be fetched" (a reply)
// from "the request never reached indexarr" (a transport error). The RPC
// wrapper owns the decode and is the only place a transport error is produced
// for this verb.
func (s *Service) Handle(ctx context.Context, req schema.DownloadRequest) schema.DownloadResponse {
	ctx, span := tracing.Start(ctx, "indexarr.download")
	defer span.End()
	start := s.now()

	resp, result, label := s.handle(ctx, req)

	metrics.IndexerQueriesTotal.WithLabelValues(label, result).Inc()
	metrics.IndexerQueryDuration.WithLabelValues(label, "download").
		Observe(s.now().Sub(start).Seconds())
	span.SetAttributes(
		attribute.String("indexer.namespace", req.IndexerRef.Namespace),
		attribute.String("indexer.name", req.IndexerRef.Name),
		attribute.String("download.result", result),
		attribute.Int("download.bytes", len(resp.Bytes)),
	)
	if resp.Error != "" {
		tracing.RecordError(span, errors.New(resp.Error))
	}
	return resp
}

// handle validates the request, then resolves and fetches. It returns the
// reply, the closed-vocabulary outcome and the bounded metric label.
func (s *Service) handle(
	ctx context.Context, req schema.DownloadRequest,
) (schema.DownloadResponse, string, string) {
	switch {
	case req.IndexerRef.Namespace == "" || req.IndexerRef.Name == "":
		// Ruling R10's hole, closed by construction. Resolving a
		// namespace-less ref cluster-wide would let namespace A's release
		// be fetched with namespace B's passkey and counted against B's
		// grab budget. This verb has zero callers today, so refusing costs
		// nothing.
		return fail(nil, "indexarr: indexerRef.namespace and indexerRef.name are required"),
			resultBadRequest, unknownIndexerLabel
	case req.GUID == "":
		return fail(nil, "indexarr: guid is required"), resultBadRequest, unknownIndexerLabel
	case req.URL == "":
		// relindex.Query has no GUID field and ADR-0003 fixes the Store at
		// four methods, so indexarr cannot resolve a guid to a URL.
		// Carried item.
		return fail(nil, "indexarr: release has no download URL; "+
			"indexarr cannot resolve a guid without one"), resultNoURL, unknownIndexerLabel
	case s.Client == nil || s.Fetch == nil:
		return fail(nil, "indexarr: download verb is not configured"),
			resultNotConfigured, unknownIndexerLabel
	}
	return s.fetchAndCount(ctx, req)
}

func (s *Service) fetchAndCount(
	ctx context.Context, req schema.DownloadRequest,
) (schema.DownloadResponse, string, string) {
	var idx indexv1alpha1.Indexer
	key := types.NamespacedName{Namespace: req.IndexerRef.Namespace, Name: req.IndexerRef.Name}
	if err := s.Client.Get(ctx, key, &idx); err != nil {
		if apierrors.IsNotFound(err) {
			return fail(nil, "indexarr: indexer %s not found", key),
				resultNotFound, unknownIndexerLabel
		}
		return fail(nil, "indexarr: read indexer %s: %v", key, err),
			resultTransport, unknownIndexerLabel
	}
	// From here the label is a Kubernetes object name: bounded cardinality.
	label := idx.Name
	if idx.Spec.Enabled != nil && !*idx.Spec.Enabled {
		return fail(nil, "indexarr: indexer %s is disabled", key), resultDisabled, label
	}
	// status.disabledUntil is deliberately NOT checked: escalation is health,
	// and this release was already selected by a search that succeeded.
	// Refusing here strands an approved grab. Download failures also do not
	// feed RecordFailure -- a dead link is a release-level fact, not an
	// indexer-level one. Carried item.

	fetchFor := s.Fetch
	if definitionBacked(&idx) {
		if s.Definitions == nil {
			return fail(nil, "indexarr: indexer %s is definition-backed and the Cardigann download path is not configured", key),
				resultNotConfigured, label
		}
		fetchFor = s.Definitions
	}
	f, err := fetchFor(ctx, &idx)
	if err != nil {
		return fail(nil, "indexarr: build client for %s: %v", key, cardigann.RedactErr(err)),
			resultTransport, label
	}
	log := logging.FromContext(ctx).With(
		"indexer", idx.Name, "namespace", idx.Namespace,
		"guid", truncate(redactRawURL(req.GUID), maxGUIDLogChars))

	res, err := f.Fetch(ctx, req.URL)
	if err != nil {
		return fail(f.Scrub, "indexarr: fetch from %s: %v", key, cardigann.RedactErr(err)),
			resultTransport, label
	}
	if res == nil {
		// Fetch is an injected interface: D1-8 supplies NewFetcherFor, a
		// test supplies a stub. A nil result with a nil error is a broken
		// implementation, and a nil dereference here would take down the
		// RPC responder rather than failing one grab.
		return fail(nil, "indexarr: fetcher for %s returned no result", key),
			resultTransport, label
	}

	resp, result := s.classify(f, res, log)
	if resp.Error == "" {
		s.countGrab(ctx, &idx, req.GUID, log)
	}
	return resp, result, label
}

// classify turns one FetchResult into the reply. DownloadResponse carries
// "exactly one of Bytes, MagnetURL or RedirectURL", so every branch sets
// exactly one -- or an Error and none.
func (s *Service) classify(
	f Fetcher, res *FetchResult, log *slog.Logger,
) (schema.DownloadResponse, string) {
	// A link result never carries a body from this package's own fetcher --
	// Fetch closes the 3xx body itself and leaves Body nil -- but a Fetcher
	// is an interface, and a future one that sets both must not leak a
	// connection because this function returned early.
	if res.Body != nil && (res.MagnetURL != "" || res.OffHostURL != "") {
		_ = res.Body.Close()
	}

	switch {
	case res.MagnetURL != "":
		log.Debug("indexarr/download: magnet link")
		return schema.DownloadResponse{MagnetURL: res.MagnetURL}, resultMagnet

	case res.OffHostURL != "":
		// Cause 1 of RedirectURL: the chain left the indexer's origin, where
		// our session cookie would not be sent anyway. Returned INTACT --
		// grabarr needs it -- and logged redacted, because it may carry a
		// passkey.
		log.Debug("indexarr/download: handing back an off-host link",
			"url", redactRawURL(res.OffHostURL))
		return schema.DownloadResponse{RedirectURL: res.OffHostURL}, resultRedirect

	case res.Body == nil:
		// A Fetcher that reports neither a link nor a body is broken, not
		// the indexer. Say so rather than dereferencing nil.
		return fail(f.Scrub, "indexarr: indexer returned HTTP %d with no body", res.Status),
			resultInvalidPayload
	}

	defer func() { _ = res.Body.Close() }()

	if res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden {
		return fail(f.Scrub, "indexarr: indexer returned HTTP %d; "+
			"the session or passkey may have expired", res.Status), resultUnauthorized
	}
	if res.Status < 200 || res.Status >= 300 {
		// The STATUS only. An error page's body is attacker-controlled text
		// on its way to a status condition.
		return fail(f.Scrub, "indexarr: indexer returned HTTP %d", res.Status), resultHTTPError
	}

	// Cause 2 of RedirectURL: too big to send. Decided on Content-Length
	// ALONE, before the body is touched, so a one-shot link is not spent on
	// bytes we would refuse.
	if res.ContentLen > MaxPayloadBytes {
		log.Info("indexarr/download: body exceeds the broker payload budget; handing back the link",
			"contentLength", res.ContentLen, "max", MaxPayloadBytes)
		return schema.DownloadResponse{RedirectURL: res.FinalURL.String()}, resultRedirect
	}

	body, err := readPayload(res.Body)
	if err != nil {
		if errors.Is(err, ErrResponseTooLarge) {
			// No Content-Length, so this was only discovered mid-read. The
			// link may already be spent, which is why this is an Error and
			// not a RedirectURL.
			return fail(f.Scrub, "indexarr: %v", err), resultTooLarge
		}
		return fail(f.Scrub, "indexarr: read payload: %v", cardigann.RedactErr(err)), resultInvalidPayload
	}

	kind := sniffKind(body)
	if kind == kindHTML {
		return fail(f.Scrub, "indexarr: indexer returned an HTML page, "+
			"not a download payload (session expired?)"), resultInvalidPayload
	}
	return schema.DownloadResponse{
		Bytes:       body,
		ContentType: contentTypeFor(res.Header.Get("Content-Type"), kind),
	}, resultOK
}

// countGrab records the grab and projects the ring into
// status.grabsInWindow.
//
// Both halves are NON-FATAL: the bytes are already in the reply, and an
// accounting outage must not strand a grab. The ring is the source of truth;
// status.grabsInWindow is a projection of it.
func (s *Service) countGrab(
	ctx context.Context, idx *indexv1alpha1.Indexer, guid string, log *slog.Logger,
) {
	now := s.now()
	// Non-fatal on this path: countGrabAt has already logged, and an
	// accounting outage must never strand a grab that already has its bytes.
	_ = s.countGrabAt(ctx, idx, guid, now, now, log)
}

// countGrabAt counts the grab of guid made at `at` into idx's ring and, when
// it was new, projects the ring's count onto status.grabsInWindow. It returns
// an error only for a failure worth retrying (the ring or the apply); the
// download verb ignores it, the direct-grab reconciler requeues on it.
func (s *Service) countGrabAt(
	ctx context.Context, idx *indexv1alpha1.Indexer, guid string, at, now time.Time, log *slog.Logger,
) error {
	ctx, span := tracing.Start(ctx, "indexarr.download.count_grab")
	defer span.End()
	if s.Bus == nil {
		return nil
	}
	n, counted, err := CountGrabAt(ctx, s.Bus.KV(events.BucketIndexerLimits), idx, guid, at, now)
	if err != nil {
		log.Warn("indexarr/download: grab accounting failed", "err", err)
		metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, resultGrabFailed).Inc()
		tracing.RecordError(span, err)
		return err
	}
	if !counted {
		// A redelivery, or a grab older than the window. The count did not
		// change, so there is nothing to apply -- and an apply that does not
		// happen releases nothing, which is the one safe shortcut under this
		// field manager.
		metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, resultGrabDuplicate).Inc()
		return nil
	}
	metrics.IndexerQueriesTotal.WithLabelValues(idx.Name, resultGrabCounted).Inc()

	// RE-READ before the apply. idx was fetched before the download, and a
	// download is one request of up to spec.timeout plus up to
	// MaxPayloadBytes of body -- seconds, not milliseconds. indexarr/status
	// seeds the apply from the status it is handed and re-sends EVERY field
	// this manager owns, so applying a pre-fetch snapshot rolls back whatever
	// else wrote under k8s.ManagerIndexarrWorker in the meantime: the search
	// fan-out and the RSS poll share this manager, and a search landing
	// mid-download would silently lose its queriesInWindow.
	//
	// disabledUntil is the one that turns a lost update into a correctness
	// bug rather than a counter blip. WorkerFields emits it only when
	// non-nil, so a stale nil snapshot does not roll it back -- it CLEARS
	// it, silently re-enabling an indexer another writer had just put into
	// backoff, and undoing the thing that exists to stop us hammering a
	// failing tracker. It self-heals only when the indexer fails AGAIN,
	// which is exactly what the backoff was avoiding.
	//
	// That is a lost update rather than a server-side-apply release, which
	// is why no "manager X released field Y" test can see it: both applies
	// declare the field, the second just declares a stale value. One Get per
	// download closes the window, as indexarr/worker/rss does for its poll.
	var fresh indexv1alpha1.Indexer
	if err := s.Client.Get(ctx, client.ObjectKeyFromObject(idx), &fresh); err != nil {
		// Non-fatal, like the rest of accounting: the ring holds the grab
		// and the projection catches up at the next one. Applying the stale
		// object instead would be the bug this Get exists to prevent.
		log.Warn("indexarr/download: re-reading the indexer before the grab apply failed",
			"err", err)
		tracing.RecordError(span, err)
		return err
	}
	if n == fresh.Status.GrabsInWindow {
		return nil
	}
	if err := idxstatus.Patch(ctx, s.Client, k8s.ManagerIndexarrWorker, &fresh,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			// COMPLETE declaration, every time. Server-side apply REPLACES a
			// manager's ownership set rather than merging it, so a field
			// this manager owned and now omits is released and reads as
			// zero. WorkerFields is the single definition of that set
			// (Ruling R14).
			//
			// This assignment is a deliberate BACKSTOP, not the primary
			// guard: Patch already seeds ac from WorkerFields, so the two
			// are redundant and either alone is sufficient. Measured, not
			// assumed -- dropping either one on its own leaves
			// TestGrabCountDoesNotReleaseTheOtherWorkerFields green, and
			// only dropping both turns queriesInWindow to 0. It is kept
			// because it makes the completeness visible where the mutate is
			// written, and because a caller that hand-built its own apply
			// configuration is exactly the defect R14 exists to prevent.
			// WorkerFields sets no list field, so re-seeding cannot double
			// entries the way a Conditions or Sidecars reassert would.
			//
			// It seeds from FRESH, never from idx: seeding from the
			// pre-fetch snapshot is the lost update above.
			*ac = *idxstatus.WorkerFields(fresh.Status)
			ac.WithGrabsInWindow(n)
		}); err != nil {
		log.Warn("indexarr/download: grabsInWindow apply failed", "err", err)
		return err
	}
	// indexer.limited when this grab filled the window, after the apply.
	idxstatus.PublishTransitions(ctx, s.Bus, &fresh, idxstatus.Transition{
		Prev: fresh.Status, Grabs: &n, At: now,
	})
	return nil
}

// CountGrabForTest drives the accounting path directly. It exists so the SSA
// release test and D1-9's e2e can exercise the status projection without a
// fetcher, a bus subject or an HTTP server.
func (s *Service) CountGrabForTest(ctx context.Context, idx *indexv1alpha1.Indexer, guid string) {
	s.countGrab(ctx, idx, guid, logging.FromContext(ctx))
}
