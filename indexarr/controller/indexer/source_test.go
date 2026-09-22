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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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

func TestLimiterKeyFor(t *testing.T) {
	require.Equal(t, "nzbgeek.info:8080", limiterKeyFor("https://nzbgeek.info:8080/api"))
	require.Equal(t, "", limiterKeyFor("::not a url"), "a malformed spec must not panic the delete path")
	require.Equal(t, "", limiterKeyFor(""))
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
				RequestDelay: metav1.Duration{Duration: 2 * time.Second},
				Timeout:      metav1.Duration{Duration: 5 * time.Second},
			}
			c, endpoint, err := buildClient(spec, nil, lim)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, c)
			require.Equal(t, tc.wantPath, endpoint.Path, "no doubled slash, no dropped base path")
			require.Equal(t, limiterKeyFor(tc.baseURL), endpoint.Host,
				"the limiter key and the client's host must be the same string")
		})
	}
}

func TestBuildClientConfiguresOneBucketPerHost(t *testing.T) {
	lim := ratelimit.New(ratelimit.Config{})
	spec := indexv1alpha1.IndexerSpec{
		BaseURL:      "https://tracker.invalid/api",
		Generic:      &indexv1alpha1.GenericNewznab{},
		RequestDelay: metav1.Duration{Duration: time.Hour},
	}
	_, _, err := buildClient(spec, nil, lim)
	require.NoError(t, err)

	// One token per hour with a burst of 1: the first Allow drains the
	// bucket and the second is refused. That is what proves SetConfig was
	// keyed on the host and actually applied.
	require.True(t, lim.Allow("tracker.invalid"))
	require.False(t, lim.Allow("tracker.invalid"))
	require.True(t, lim.Allow("other.invalid"), "an unrelated host draws from its own bucket")
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
