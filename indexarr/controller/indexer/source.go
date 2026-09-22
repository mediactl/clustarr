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

package indexer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// sourceKind is which of spec.generic, spec.definition and spec.definitionRef
// an Indexer is driven by.
type sourceKind int

const (
	sourceGeneric sourceKind = iota
	sourceDefinition
	sourceDefinitionRef
)

// resolveSource re-checks the spec's type-level CEL rule in Go. An invalid
// spec is a reconcile.TerminalError, not a requeue: no amount of retrying
// fixes it and a hot loop on a typo is how a controller burns an apiserver.
//
// Re-checking a rule the apiserver already enforces is not redundant: a
// reconciler that trusts a CEL rule it did not write is one apiserver
// upgrade, or one object created before the rule existed, from a nil
// dereference.
func resolveSource(spec indexv1alpha1.IndexerSpec) (sourceKind, error) {
	var set int
	kind := sourceGeneric
	if spec.Generic != nil {
		set, kind = set+1, sourceGeneric
	}
	if spec.Definition != nil {
		set, kind = set+1, sourceDefinition
	}
	if spec.DefinitionRef != nil {
		set, kind = set+1, sourceDefinitionRef
	}
	if set != 1 {
		return kind, fmt.Errorf("indexer: exactly one of spec.definition, spec.definitionRef or spec.generic must be set, found %d", set)
	}
	return kind, nil
}

// Privacy classes. They match IndexerDefinition's DefinitionType enum
// (public|semiPrivate|private) so the two status fields read the same, even
// though status.privacy itself carries no enum marker. Note that
// pkg/cardigann.DefinitionType spells the middle one "semi-private"; M6 maps
// between them.
const (
	PrivacyPublic      = "public"
	PrivacySemiPrivate = "semiPrivate"
	PrivacyPrivate     = "private"
)

// protocolFor resolves status.protocol. It returns "" for a definition-backed
// Indexer, whose protocol comes from the Cardigann definition (M6). The
// caller MUST omit status.protocol when this returns "": the CRD schema is
// enum: [torrent, usenet] and an explicit "" is rejected.
func protocolFor(kind sourceKind, spec indexv1alpha1.IndexerSpec) commonv1alpha1.Protocol {
	if kind == sourceGeneric && spec.Generic != nil {
		return spec.Generic.Protocol
	}
	return ""
}

// privacyFor resolves status.privacy. §6.2 resolves it "from the definition",
// and a generic upstream has none -- so it is derived from whether the
// upstream is credentialled. Prowlarr's own generic Newznab and Torznab
// indexers are both IndexerPrivacy.Private, because a generic upstream is
// nearly always an API-keyed private tracker or a Prowlarr/Jackett proxy in
// front of one; an uncredentialled one is a public index.
func privacyFor(kind sourceKind, secret map[string][]byte) string {
	if kind != sourceGeneric {
		return "" // M6 reads it off the definition
	}
	if len(secret["apikey"]) > 0 || len(secret["passkey"]) > 0 || len(secret["cookie"]) > 0 {
		return PrivacyPrivate
	}
	return PrivacyPublic
}

// sessionSecretSuffix names the Secret that holds an indexer's login session
// (Cardigann cookies and JWTs, mirrored from the clustarr-indexer-sessions KV
// bucket). The reconciler publishes the name in status.sessionSecretRef; M6's
// login flow creates and fills it.
const sessionSecretSuffix = "-session"

// maxObjectName is a DNS subdomain's limit (RFC 1123).
const maxObjectName = 253

// sessionSecretName is deterministic, and the reconciler therefore sends it
// on EVERY apply whether or not the Secret exists. A reference that is
// sometimes sent and sometimes omitted is the "a boolean stopped being sent
// once it was true" release trap in another costume.
func sessionSecretName(indexerName string) string {
	if len(indexerName)+len(sessionSecretSuffix) <= maxObjectName {
		return indexerName + sessionSecretSuffix
	}
	return indexerName[:maxObjectName-len(sessionSecretSuffix)] + sessionSecretSuffix
}

// readSecret reads spec.secretRef. The recognised keys are apikey, username,
// password, cookie, passkey and rss_key (indexer_types.go). A missing Secret
// is a dependency error, not a terminal one: the operator may well be
// creating it in the next kubectl apply.
func readSecret(ctx context.Context, c client.Client, ns string, ref *corev1.LocalObjectReference) (map[string][]byte, error) {
	if ref == nil {
		return nil, nil
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("indexer: secret %s/%s not found", ns, ref.Name)
		}
		return nil, err
	}
	return s.Data, nil
}

// CRD defaults, restated here because a kubebuilder default reaches far
// fewer objects than it looks like it does -- see timeoutFor.
// TestSpecDefaultsMatchTheGeneratedCRD reads the generated schema and fails
// if either drifts, so this is a mirror rather than a second source of
// truth.
const (
	// defaultTimeout mirrors spec.timeout's +kubebuilder:default="30s".
	defaultTimeout = 30 * time.Second

	// defaultRequestDelay mirrors spec.requestDelay's
	// +kubebuilder:default="2s". It is documentation and a test anchor
	// only: see rpsFor for why requestDelay is NOT floored.
	defaultRequestDelay = 2 * time.Second
)

// timeoutFor floors spec.timeout.
//
// torznab.NewClient seeds its own 30s default and THEN applies the options,
// so WithTimeout(0) OVERWRITES that default with "no timeout" -- an indexer
// that accepts the connection and never answers would hold the reconcile
// until the controller's own 5-minute ReconciliationTimeout fired, once per
// tick, per indexer.
//
// The CRD's +kubebuilder:default does NOT cover this, and it is worth being
// precise about why, because the obvious reading is wrong. An apiserver
// default fills a field that is ABSENT FROM THE SUBMITTED JSON. metav1.Duration
// is a struct, and `omitempty` does nothing to a struct field, so a typed Go
// client ALWAYS marshals it: an Indexer created through client-go sends
// `"timeout":"0s"` explicitly and is never defaulted. Only YAML and
// unstructured creates -- kubectl apply, the chart -- omit the key and get
// 30s. Verified against a real apiserver: an unstructured create yields
// timeout=30s, requestDelay=2s, rssInterval=15m, and a typed create yields
// 0s/0s/0s. Every envtest in this package is a typed create, and so is any
// future in-cluster creator.
func timeoutFor(timeout metav1.Duration) time.Duration {
	if timeout.Duration <= 0 {
		return defaultTimeout
	}
	return timeout.Duration
}

// rpsFor converts spec.requestDelay (default 2s) into a token-bucket rate.
// ratelimit.Config treats RPS <= 0 as unlimited and Burst <= 0 as 1.
//
// Unlike timeoutFor, this deliberately does NOT floor at the CRD default.
// An explicit `requestDelay: 0s` survives defaulting, and
// ratelimit.Config documents RPS <= 0 as unlimited on purpose -- that pair
// is a supported "do not pace this indexer", and flooring here would
// silently overrule the operator. The zero timeout has no such reading:
// nobody wants a probe that never returns.
//
// # Known limitation: "0s" is ambiguous on the wire, and it is not fixable here
//
// Exactly the same `"requestDelay":"0s"` arrives from two producers that
// mean incompatible things, and nothing downstream can tell them apart:
//
//	an operator writing requestDelay: 0s   -> "do not pace this indexer"
//	a Go client leaving the field at zero  -> "I did not specify one"
//
// The second is the more common producer inside this codebase, because
// metav1.Duration is a struct and `omitempty` does nothing to a struct
// field, so client-go always marshals the key (see timeoutFor). The live
// consequence is that an Indexer created IN-CLUSTER by a typed client is
// silently UNPACED, against §6.2's 2s-per-host intent -- while the same
// Indexer applied as YAML gets its 2s.
//
// This is latent today: nothing creates Indexers in-cluster yet. M6's
// definition ingestion and any UI create path will hit it.
//
// It cannot be fixed in this package. Distinguishing absent from
// explicit-zero needs *metav1.Duration in IndexerSpec or a defaulting
// webhook, both of which live in api/index/**, which Phase D1 task D1-3
// does not own. Filed as a carry-forward. Do NOT "fix" it here by
// flooring: that trades a latent unpaced indexer for an operator whose
// explicit choice is ignored, which is worse because it is unfixable from
// the outside.
func rpsFor(delay metav1.Duration) float64 {
	if delay.Duration <= 0 {
		return 0
	}
	return 1 / delay.Seconds()
}

// limiterKeyFor is the indexer HOST, not the object name: two Indexers
// pointing at one tracker must share one bucket, which is the whole reason
// the limiter is injected rather than built per client. It is also the key
// pkg/torznab's client uses internally (its own baseURL host), so the two
// cannot disagree -- and buildClient below calls this rather than reaching
// for u.Host itself, so there is one definition of what the key is.
//
// It returns "" rather than an error for a malformed URL: every caller
// already has, or is about to produce, a better error about the URL itself,
// and a key helper that can fail is a key helper callers skip.
func limiterKeyFor(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// buildClient assembles the Torznab client for one Indexer and returns the
// resolved API endpoint alongside it.
//
// The limiter is injected, never defaulted: pkg/torznab's package doc makes
// the caller the owner of pacing, and a library-side default would sit in
// series underneath this one and silently change the effective rate. This
// reconciler is the only writer of a key's Config because it is the only
// reader of spec.requestDelay; the search fan-out and the RSS poll share the
// same *ratelimit.Limiter instance and only Wait on it.
func buildClient(spec indexv1alpha1.IndexerSpec, secret map[string][]byte, lim *ratelimit.Limiter) (*torznab.Client, *url.URL, error) {
	u, err := url.Parse(spec.BaseURL)
	if err != nil {
		return nil, nil, fmt.Errorf("indexer: parse spec.baseURL %q: %w", spec.BaseURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, nil, fmt.Errorf("indexer: spec.baseURL %q must be absolute", spec.BaseURL)
	}
	apiPath := "/api"
	if spec.Generic != nil && spec.Generic.APIPath != "" {
		apiPath = spec.Generic.APIPath
	}
	// url.URL.JoinPath on a URL with an EMPTY path yields a path with no
	// leading slash ("api", not "/api"). URL.String() papers over it when
	// it re-renders the request, but the *url.URL handed back to the caller
	// -- and to any future proxy or facade code that inspects .Path --
	// would be wrong. Normalising the base first keeps the two consistent.
	base := *u
	if base.Path == "" {
		base.Path = "/"
	}
	endpoint := base.JoinPath(apiPath)

	// A nil limiter disables pacing rather than panicking, the same way a
	// nil Recorder disables events: it is what lets a unit test build a
	// Reconciler with nothing but a client. D1-8 always supplies one.
	if lim != nil {
		lim.SetConfig(limiterKeyFor(spec.BaseURL), ratelimit.Config{RPS: rpsFor(spec.RequestDelay), Burst: 1})
	}

	opts := []torznab.ClientOption{torznab.WithTimeout(timeoutFor(spec.Timeout))}
	if lim != nil {
		// D1-1 reshaped this option: the limiter carries no key argument,
		// because the client keys it on its own baseURL host -- the same
		// string limiterKeyFor returns.
		opts = append(opts, torznab.WithRateLimit(lim))
	}

	c, err := torznab.NewClient(endpoint.String(), string(secret["apikey"]), opts...)
	if err != nil {
		return nil, nil, err
	}
	return c, endpoint, nil
}

// probeOutcome is what one caps probe told us.
type probeOutcome struct {
	Reason     string // a condition reason; "" on success
	Message    string
	AuthFailed bool
	Limited    bool
	RetryAfter time.Duration // from the Retry-After header on a 429
}

// Condition reasons local to Indexer; see pkg/k8s.Reason* for the shared set.
const (
	ReasonDefinitionNotImplemented = "DefinitionNotImplemented"
	ReasonCredentialsRejected      = "CredentialsRejected"
	ReasonIndexerDisabled          = "IndexerDisabled"
	ReasonResponseTooLarge         = "ResponseTooLarge"
	ReasonProbeFailed              = "ProbeFailed"
	ReasonBackingOff               = "BackingOff"
	ReasonLimitReached             = "LimitReached"
)

// classify is the ONE place *torznab.Error is decoded, so the conditions and
// the requeue delay cannot disagree about what a 429 means.
func classify(err error) probeOutcome {
	if err == nil {
		return probeOutcome{}
	}
	out := probeOutcome{Reason: ReasonProbeFailed, Message: err.Error()}
	if errors.Is(err, torznab.ErrResponseTooLarge) {
		out.Reason = ReasonResponseTooLarge
		return out
	}
	var te *torznab.Error
	if !errors.As(err, &te) {
		return out
	}
	switch {
	case te.Code == torznab.ErrIncorrectCredentials ||
		te.Code == torznab.ErrAccountSuspended ||
		te.Code == torznab.ErrInsufficientPrivileges ||
		te.HTTPStatus == http.StatusUnauthorized || te.HTTPStatus == http.StatusForbidden:
		out.Reason, out.AuthFailed = ReasonCredentialsRejected, true
	case te.Code == torznab.ErrRequestLimitReached ||
		te.Code == torznab.ErrDownloadLimitReached ||
		te.HTTPStatus == http.StatusTooManyRequests:
		out.Reason, out.Limited, out.RetryAfter = ReasonLimitReached, true, te.RetryAfter
	case te.HTTPStatus == http.StatusGone: // Prowlarr's "indexer disabled"
		out.Reason = ReasonIndexerDisabled
	}
	return out
}

// outcomeLabel maps a probe outcome onto the small closed set
// metrics.IndexerQueriesTotal's `outcome` label may carry. It never returns
// an indexer-supplied string: a label value taken from a remote server's
// error text is unbounded cardinality, which is how a Prometheus falls over.
func outcomeLabel(o probeOutcome) string {
	switch {
	case o.Reason == "":
		return "ok"
	case o.AuthFailed:
		return "unauthorized"
	case o.Limited:
		return "rate_limited"
	case o.Reason == ReasonIndexerDisabled:
		return "banned"
	default:
		return "error"
	}
}
