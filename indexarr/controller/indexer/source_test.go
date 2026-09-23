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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestResolveSource(t *testing.T) {
	def := "1337x"
	cases := []struct {
		name    string
		spec    indexv1alpha1.IndexerSpec
		want    sourceKind
		wantErr bool
	}{
		{name: "generic", spec: indexv1alpha1.IndexerSpec{Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent}}, want: sourceGeneric},
		{name: "bundled definition", spec: indexv1alpha1.IndexerSpec{Definition: &def}, want: sourceDefinition},
		{name: "definitionRef", spec: indexv1alpha1.IndexerSpec{DefinitionRef: &def}, want: sourceDefinitionRef},
		{name: "none", spec: indexv1alpha1.IndexerSpec{}, wantErr: true},
		{name: "two", spec: indexv1alpha1.IndexerSpec{Definition: &def, Generic: &indexv1alpha1.GenericNewznab{}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSource(tc.spec)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestProtocolForIsEmptyForADefinitionBackedIndexer(t *testing.T) {
	def := "1337x"
	require.Equal(t, commonv1alpha1.ProtocolUsenet,
		protocolFor(sourceGeneric, indexv1alpha1.IndexerSpec{Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolUsenet}}))
	require.Equal(t, commonv1alpha1.Protocol(""),
		protocolFor(sourceDefinition, indexv1alpha1.IndexerSpec{Definition: &def}),
		"status.protocol is enum:[torrent,usenet]; the caller must OMIT it, not send an empty string")
}

func TestPrivacyFor(t *testing.T) {
	require.Equal(t, PrivacyPrivate, privacyFor(sourceGeneric, map[string][]byte{"apikey": []byte("k")}))
	require.Equal(t, PrivacyPrivate, privacyFor(sourceGeneric, map[string][]byte{"passkey": []byte("k")}))
	require.Equal(t, PrivacyPrivate, privacyFor(sourceGeneric, map[string][]byte{"cookie": []byte("k")}))
	require.Equal(t, PrivacyPublic, privacyFor(sourceGeneric, nil))
	require.Equal(t, PrivacyPublic, privacyFor(sourceGeneric, map[string][]byte{"apikey": nil}),
		"an empty value is not a credential")
	require.Equal(t, "", privacyFor(sourceDefinition, map[string][]byte{"apikey": []byte("k")}))
}

func TestSessionSecretNameIsDeterministicAndFitsADNSSubdomain(t *testing.T) {
	require.Equal(t, "nzbgeek-session", sessionSecretName("nzbgeek"))
	long := strings.Repeat("a", 253)
	got := sessionSecretName(long)
	require.LessOrEqual(t, len(got), maxObjectName)
	require.True(t, strings.HasSuffix(got, sessionSecretSuffix))
	require.Equal(t, got, sessionSecretName(long), "the name must not drift between reconciles")
}

func TestReadSecret(t *testing.T) {
	scheme := k8s.MustNewScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "media"},
		Data:       map[string][]byte{"apikey": []byte("abc")},
	}).Build()
	ctx := context.Background()

	got, err := readSecret(ctx, c, "media", &corev1.LocalObjectReference{Name: "creds"})
	require.NoError(t, err)
	require.Equal(t, []byte("abc"), got["apikey"])

	got, err = readSecret(ctx, c, "media", nil)
	require.NoError(t, err, "no secretRef is not an error")
	require.Nil(t, got)

	_, err = readSecret(ctx, c, "media", &corev1.LocalObjectReference{Name: "missing"})
	require.ErrorContains(t, err, "media/missing")
}

func TestRpsFor(t *testing.T) {
	require.InDelta(t, 0.5, rpsFor(metav1.Duration{Duration: 2 * time.Second}), 1e-9)
	require.InDelta(t, 10.0, rpsFor(metav1.Duration{Duration: 100 * time.Millisecond}), 1e-9)
	require.Equal(t, 0.0, rpsFor(metav1.Duration{}), "no delay means unlimited")
	require.Equal(t, 0.0, rpsFor(metav1.Duration{Duration: -time.Second}))
}

// TestTheLimiterKeyIsRatelimitHostKey pins the RECONCILER half of a
// convention that has two halves. The other half is indexarr/download's
// TestTheFetcherKeysItsLimiterWithRatelimitHostKey, and both anchor on
// ratelimit.HostKey so neither can drift on its own (ruling R38).
//
// This reconciler is the only writer of a key's Config and the download verb
// only Waits on it. If the two spelled the key differently the Wait would land
// on a key with NO Config and ratelimit would fall back to the Limiter's
// defaults.
//
// # The canary is quieter than it used to be, on purpose
//
// This comment used to say "and D1-8 builds the Limiter as
// ratelimit.New(ratelimit.Config{})", so a divergent key meant a COMPLETELY
// unpaced download verb. It no longer does: indexarr.defaultLimiterConfig
// paces an unknown host at spec.requestDelay's CRD default, precisely so a
// host nobody has reconciled yet is not hammered. The trade is deliberate and
// worth naming -- a divergent key is now silently paced at 2s instead of
// loudly unpaced, so the deliberately-unlimited Limiter constructed HERE is
// the only place the divergence is still detectable. Do not "fix" this test
// by switching it to the production default.
//
// The helper's own table (host[:port] only, "" on a malformed URL) moved to
// pkg/ratelimit's TestHostKey with the function.
func TestTheLimiterKeyIsRatelimitHostKey(t *testing.T) {
	// An unlimited default, so a divergent key is unpaced rather than merely
	// differently paced and this test fails loudly instead of subtly. See the
	// note above: production's default is finite.
	lim := ratelimit.New(ratelimit.Config{})
	spec := indexv1alpha1.IndexerSpec{
		// A port and a path, because both are places a hand-rolled key
		// drifts: u.Hostname() drops the port, u.String() keeps the path.
		BaseURL:      "https://tracker.invalid:8443/prowlarr/1",
		Generic:      &indexv1alpha1.GenericNewznab{},
		RequestDelay: &metav1.Duration{Duration: time.Hour},
	}
	applyRateLimit(spec, lim, 0)

	key := ratelimit.HostKey(spec.BaseURL)
	require.True(t, lim.Allow(key))
	require.False(t, lim.Allow(key),
		"applyRateLimit configured some OTHER key, so ratelimit.HostKey names an unconfigured bucket")
}

func TestBuildClient(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		apiPath  string
		wantErr  string
		wantPath string
	}{
		{name: "default apiPath", baseURL: "https://nzbgeek.info", wantPath: "/api"},
		{name: "explicit apiPath", baseURL: "https://nzbgeek.info", apiPath: "/api/v2", wantPath: "/api/v2"},
		{name: "trailing slash", baseURL: "https://nzbgeek.info/", apiPath: "/api", wantPath: "/api"},
		{name: "base with a path", baseURL: "https://host/prowlarr/1", apiPath: "/api", wantPath: "/prowlarr/1/api"},
		{name: "relative", baseURL: "nzbgeek.info", wantErr: "must be absolute"},
		{name: "unparseable", baseURL: "http://[::1", wantErr: "parse spec.baseURL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lim := ratelimit.New(ratelimit.Config{})
			spec := indexv1alpha1.IndexerSpec{
				BaseURL:      tc.baseURL,
				Generic:      &indexv1alpha1.GenericNewznab{APIPath: tc.apiPath},
				RequestDelay: &metav1.Duration{Duration: 2 * time.Second},
				Timeout:      metav1.Duration{Duration: 5 * time.Second},
			}
			c, endpoint, err := buildClient(spec, nil, lim, nil)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, c)
			require.Equal(t, tc.wantPath, endpoint.Path, "no doubled slash, no dropped base path")
			require.Equal(t, ratelimit.HostKey(tc.baseURL), endpoint.Host,
				"the limiter key and the client's host must be the same string")
		})
	}
}

func TestApplyRateLimitConfiguresOneBucketPerHost(t *testing.T) {
	lim := ratelimit.New(ratelimit.Config{})
	spec := indexv1alpha1.IndexerSpec{
		BaseURL:      "https://tracker.invalid/api",
		Generic:      &indexv1alpha1.GenericNewznab{},
		RequestDelay: &metav1.Duration{Duration: time.Hour},
	}
	applyRateLimit(spec, lim, 0)

	// One token per hour with a burst of 1: the first Allow drains the
	// bucket and the second is refused. That is what proves SetConfig was
	// keyed on the host and actually applied.
	require.True(t, lim.Allow("tracker.invalid"))
	require.False(t, lim.Allow("tracker.invalid"))
	require.True(t, lim.Allow("other.invalid"), "an unrelated host draws from its own bucket")
}

// TestBuildClientWritesNoLimiterConfig is the other half of the split, and it
// is the half that protects an operator's edit.
//
// buildClient is shared with ClientCache, which the search fan-out and the RSS
// poll call on every query. If it still wrote SetConfig, both would become
// writers of limiter config and would re-apply spec.requestDelay from their
// own possibly-stale cached Indexer -- so lowering the delay would be silently
// reverted by the next search. Only Reconcile writes, through applyRateLimit.
func TestBuildClientWritesNoLimiterConfig(t *testing.T) {
	lim := ratelimit.New(ratelimit.Config{})
	spec := indexv1alpha1.IndexerSpec{
		BaseURL:      "https://tracker.invalid/api",
		Generic:      &indexv1alpha1.GenericNewznab{},
		RequestDelay: &metav1.Duration{Duration: time.Hour},
	}
	_, _, err := buildClient(spec, nil, lim, nil)
	require.NoError(t, err)

	// The Limiter's default here is unlimited, so if buildClient had written
	// the one-per-hour Config the second Allow would be refused.
	require.True(t, lim.Allow("tracker.invalid"))
	require.True(t, lim.Allow("tracker.invalid"),
		"buildClient wrote a limiter Config; it is shared with ClientCache, so the search "+
			"fan-out and the RSS poll would each re-apply a possibly-stale spec.requestDelay")
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		reason     string
		auth       bool
		limited    bool
		retryAfter time.Duration
	}{
		{name: "nil", err: nil, reason: ""},
		{name: "bare", err: errors.New("dial tcp: connection refused"), reason: ReasonProbeFailed},
		{name: "too large", err: fmt.Errorf("%w: at least 9 bytes", torznab.ErrResponseTooLarge), reason: ReasonResponseTooLarge},
		{name: "malformed caps", err: fmt.Errorf("%w: strconv.ParseInt: invalid syntax", torznab.ErrMalformedCaps), reason: ReasonMalformedCaps},
		{name: "bad credentials", err: &torznab.Error{Code: torznab.ErrIncorrectCredentials}, reason: ReasonCredentialsRejected, auth: true},
		{name: "suspended", err: &torznab.Error{Code: torznab.ErrAccountSuspended}, reason: ReasonCredentialsRejected, auth: true},
		{name: "insufficient privileges", err: &torznab.Error{Code: torznab.ErrInsufficientPrivileges}, reason: ReasonCredentialsRejected, auth: true},
		{name: "http 401", err: &torznab.Error{HTTPStatus: http.StatusUnauthorized}, reason: ReasonCredentialsRejected, auth: true},
		{name: "http 403", err: &torznab.Error{HTTPStatus: http.StatusForbidden}, reason: ReasonCredentialsRejected, auth: true},
		{name: "http 429", err: &torznab.Error{HTTPStatus: http.StatusTooManyRequests, RetryAfter: 90 * time.Second}, reason: ReasonLimitReached, limited: true, retryAfter: 90 * time.Second},
		{name: "download limit", err: &torznab.Error{Code: torznab.ErrDownloadLimitReached}, reason: ReasonLimitReached, limited: true},
		{name: "request limit", err: &torznab.Error{Code: torznab.ErrRequestLimitReached}, reason: ReasonLimitReached, limited: true},
		{name: "http 410", err: &torznab.Error{HTTPStatus: http.StatusGone}, reason: ReasonIndexerDisabled},
		{name: "wrapped torznab error", err: fmt.Errorf("probe: %w", &torznab.Error{Code: torznab.ErrIncorrectCredentials}), reason: ReasonCredentialsRejected, auth: true},
		{name: "other code", err: &torznab.Error{Code: torznab.ErrUnknown}, reason: ReasonProbeFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.err)
			require.Equal(t, tc.reason, got.Reason)
			require.Equal(t, tc.auth, got.AuthFailed)
			require.Equal(t, tc.limited, got.Limited)
			require.Equal(t, tc.retryAfter, got.RetryAfter)
			if tc.err == nil {
				require.Empty(t, got.Message)
			} else {
				require.NotEmpty(t, got.Message)
			}
		})
	}
}

// The outcome label is a Prometheus label value: it must come from a small
// closed set, never from an indexer-supplied string.
func TestOutcomeLabelIsABoundedSet(t *testing.T) {
	require.Equal(t, "ok", outcomeLabel(probeOutcome{}))
	require.Equal(t, "unauthorized", outcomeLabel(probeOutcome{Reason: ReasonCredentialsRejected, AuthFailed: true}))
	require.Equal(t, "rate_limited", outcomeLabel(probeOutcome{Reason: ReasonLimitReached, Limited: true}))
	require.Equal(t, "banned", outcomeLabel(probeOutcome{Reason: ReasonIndexerDisabled}))
	require.Equal(t, "error", outcomeLabel(probeOutcome{Reason: ReasonProbeFailed, Message: "\x00 whatever the indexer said"}))
}

// The two CRD defaults restated in source.go must equal what controller-gen
// actually generated, or the floor drifts away from the value every
// apiserver-created Indexer gets. Read from the generated schema rather than
// from the marker, because the schema is what is installed.
func TestSpecDefaultsMatchTheGeneratedCRD(t *testing.T) {
	raw, err := os.ReadFile("../../../config/crd/bases/index.clustarr.io_indexers.yaml")
	require.NoError(t, err)

	var crd struct {
		Spec struct {
			Versions []struct {
				Name   string `json:"name"`
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties map[string]struct {
									Default string `json:"default"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions, "the CRD was not parsed; run `make manifests`")

	props := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties
	require.Equal(t, defaultTimeout.String(), props["timeout"].Default,
		"source.go's defaultTimeout no longer mirrors spec.timeout's +kubebuilder:default")
	require.Equal(t, defaultRequestDelay.String(), props["requestDelay"].Default,
		"source.go's defaultRequestDelay no longer mirrors spec.requestDelay's +kubebuilder:default")
}

// A kubebuilder default fills an ABSENT field, so it never reaches a spec
// built in Go. torznab.NewClient seeds its own 30s default and THEN applies
// the options, so WithTimeout(0) would overwrite it with "no timeout".
func TestTimeoutForFloorsAtTheCRDDefault(t *testing.T) {
	require.Equal(t, defaultTimeout, timeoutFor(metav1.Duration{}))
	require.Equal(t, defaultTimeout, timeoutFor(metav1.Duration{Duration: -time.Second}))
	require.Equal(t, 5*time.Second, timeoutFor(metav1.Duration{Duration: 5 * time.Second}))
}

// requestDelay is deliberately NOT floored: an explicit `requestDelay: 0s`
// survives defaulting, and ratelimit.Config documents RPS <= 0 as unlimited
// on purpose, so the pair is a supported "do not pace this indexer".
func TestRpsForDoesNotFloorAtTheCRDDefault(t *testing.T) {
	require.Equal(t, 0.0, rpsFor(metav1.Duration{}),
		"an explicit requestDelay of 0s is the operator asking for no pacing")
	require.InDelta(t, 1/defaultRequestDelay.Seconds(), rpsFor(metav1.Duration{Duration: defaultRequestDelay}), 1e-9)
}

// spec.requestDelay is a pointer so the two meanings of "0s" come apart: an
// unset field (what a Go client that never set it sends) is the CRD default,
// and only an explicit zero reaches rpsFor as the operator's "do not pace".
func TestRequestDelayForDefaultsOnlyAnUnsetField(t *testing.T) {
	require.Equal(t, defaultRequestDelay, requestDelayFor(indexv1alpha1.IndexerSpec{}).Duration,
		"an unset requestDelay must be paced at the CRD default, not left unpaced")
	require.Equal(t, time.Duration(0), requestDelayFor(indexv1alpha1.IndexerSpec{RequestDelay: &metav1.Duration{}}).Duration,
		"an explicit requestDelay of 0s must survive: it is a supported \"do not pace this indexer\"")
	require.Equal(t, 5*time.Second,
		requestDelayFor(indexv1alpha1.IndexerSpec{RequestDelay: &metav1.Duration{Duration: 5 * time.Second}}).Duration)
}

// A nil limiter disables pacing rather than panicking, the same way a nil
// Recorder disables events. D1-8 always supplies one; a unit test need not.
func TestBuildClientToleratesANilLimiter(t *testing.T) {
	spec := indexv1alpha1.IndexerSpec{
		BaseURL:      "https://tracker.invalid",
		Generic:      &indexv1alpha1.GenericNewznab{},
		RequestDelay: &metav1.Duration{Duration: 2 * time.Second},
	}
	require.NotPanics(t, func() {
		c, endpoint, err := buildClient(spec, nil, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, c)
		require.Equal(t, "/api", endpoint.Path)
	})
}
